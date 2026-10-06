package routing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/demo"
)

func newLocal(t *testing.T, ready func(n int) error) (*LocalApplier, string) {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	if err := os.WriteFile(base, []byte("# base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	a := &LocalApplier{
		Base:    base,
		Path:    filepath.Join(dir, "aigw", "config.yaml"),
		Restart: "echo restarted >> " + filepath.Join(dir, "restarts"),
		Log:     "tail -n 20 " + filepath.Join(dir, "aigw.log"),
		Ready: func(context.Context) error {
			restarts++
			return ready(restarts)
		},
	}
	return a, dir
}

func restarts(t *testing.T, dir string) int {
	b, _ := os.ReadFile(filepath.Join(dir, "restarts"))
	return strings.Count(string(b), "restarted")
}

func TestLocalApplierWritesAndRestarts(t *testing.T) {
	a, dir := newLocal(t, func(int) error { return nil })
	desired := Compile(demo.Backends, demo.Routes)
	if err := a.Apply(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if n := restarts(t, dir); n != 1 {
		t.Errorf("restarted %d times, want 1", n)
	}
	running, err := a.Running(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cs := Diff(running, desired); len(cs) != 0 {
		t.Errorf("running differs from what was applied: %+v", cs)
	}
	b, _ := os.ReadFile(a.Path)
	if !strings.HasPrefix(string(b), "# base\n") {
		t.Errorf("config doesn't start with the base:\n%.200s", b)
	}
}

func TestRunningBeforeAnyApply(t *testing.T) {
	a, _ := newLocal(t, func(int) error { return nil })
	running, err := a.Running(context.Background())
	if err != nil || len(running) != 0 {
		t.Errorf("Running() = %d objects, %v; want none, nil", len(running), err)
	}
}

// When the gateway doesn't come back on the new config, the old one goes
// back and the gateway restarts on it; the error quotes the gateway's log.
func TestLocalApplierRollsBack(t *testing.T) {
	a, dir := newLocal(t, func(n int) error {
		if n == 2 { // the second apply's restart; the rollback's (3) comes up
			return errors.New("gateway didn't answer on :1975")
		}
		return nil
	})
	if err := a.Apply(context.Background(), Compile(demo.Backends[:1], nil)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(a.Path)
	_ = os.WriteFile(filepath.Join(dir, "aigw.log"), []byte("line 1\nerror: unknown field \"spec.bogus\"\n"), 0o644)

	err := a.Apply(context.Background(), Compile(demo.Backends, demo.Routes))
	if err == nil {
		t.Fatal("Apply succeeded with a gateway that didn't come back")
	}
	for _, want := range []string{"gateway didn't answer on :1975", `unknown field "spec.bogus"`, "rolled back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	after, _ := os.ReadFile(a.Path)
	if string(after) != string(before) {
		t.Errorf("config wasn't rolled back")
	}
	if n := restarts(t, dir); n != 3 {
		t.Errorf("restarted %d times, want 3 (first apply, failed apply, rollback)", n)
	}
}

func TestLocalApplierRestartFails(t *testing.T) {
	a, _ := newLocal(t, func(int) error { return nil })
	a.Restart = "echo 'aigw not found' >&2; exit 1"
	err := a.Apply(context.Background(), Compile(demo.Backends, demo.Routes))
	if err == nil || !strings.Contains(err.Error(), "aigw not found") {
		t.Fatalf("err = %v, want the restart command's output", err)
	}
}

func TestLocalApplierNotReady(t *testing.T) {
	a := &LocalApplier{}
	if err := a.CanApply(); err == nil {
		t.Errorf("an applier with no restart command says it can apply")
	}
}

// An apply promotes staged provider keys with the config, before the restart
// that loads them; a failed apply rolls both back.
func TestLocalApplierPromotesKeys(t *testing.T) {
	fail := false
	a, dir := newLocal(t, func(int) error {
		if fail {
			fail = false // the rollback's restart comes up
			return errors.New("gateway didn't answer")
		}
		return nil
	})
	keys := &LocalKeyFile{Path: filepath.Join(dir, "aigw", "provider-keys.env")}
	a.Keys = keys
	_ = keys.Put("K_A", "sk-first-aaaaaaaaaaaa")
	if err := a.Apply(context.Background(), Compile(demo.Backends[:1], nil)); err != nil {
		t.Fatal(err)
	}
	if !contains(t, keys.Path, "K_A=sk-first") {
		t.Fatalf("the apply didn't promote the staged key")
	}

	_ = keys.Put("K_A", "sk-second-bbbbbbbbbbbb")
	fail = true
	if err := a.Apply(context.Background(), Compile(demo.Backends, demo.Routes)); err == nil {
		t.Fatal("apply succeeded")
	}
	if !contains(t, keys.Path, "K_A=sk-first") || contains(t, keys.Path, "sk-second") {
		t.Errorf("the failed apply left the new key live")
	}
	if !contains(t, keys.StagedPath(), "sk-second") {
		t.Errorf("the failed apply dropped the staged key")
	}
}
