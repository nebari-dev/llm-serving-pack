package routing

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Applier puts compiled routing in front of the gateway and reports what the
// gateway runs. The local one writes aigw's config file and restarts aigw; a
// Kubernetes one would apply the resources with server-side apply.
type Applier interface {
	// Target says where an apply goes, for the console.
	Target() string
	// CanApply is nil when Apply can run, else why it can't.
	CanApply() error
	// Running is the routing the gateway was last started with.
	Running(ctx context.Context) ([]Object, error)
	// Apply makes desired what the gateway runs, or leaves it as it was and
	// says why not.
	Apply(ctx context.Context, desired []Object) error
}

// LocalApplier runs aigw from a config file: Base (the infrastructure) plus
// the compiled routing, written to Path. Restart is a shell command that
// restarts aigw on Path; Ready says when it answers again. Log is a shell
// command printing the end of aigw's log, quoted when it doesn't come back.
type LocalApplier struct {
	Base, Path, Restart, Log string
	Ready                    func(context.Context) error
}

func (a *LocalApplier) Target() string { return "aigw's config file " + a.Path + ", then a restart" }

func (a *LocalApplier) CanApply() error {
	if a.Restart == "" {
		return errors.New("no command to restart the gateway is configured (stargate-api serve -aigw-restart)")
	}
	if _, err := os.Stat(a.Base); err != nil {
		return fmt.Errorf("the gateway's base config is missing: %v", err)
	}
	return nil
}

func (a *LocalApplier) Running(context.Context) ([]Object, error) {
	b, err := os.ReadFile(a.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Owned(b)
}

// Config is the whole file Apply would write.
func (a *LocalApplier) Config(desired []Object) ([]byte, error) {
	base, err := os.ReadFile(a.Base)
	if err != nil {
		return nil, err
	}
	return Render(base, desired), nil
}

func (a *LocalApplier) Apply(ctx context.Context, desired []Object) error {
	if err := a.CanApply(); err != nil {
		return err
	}
	next, err := a.Config(desired)
	if err != nil {
		return err
	}
	prev, err := os.ReadFile(a.Path)
	hadPrev := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := writeFile(a.Path, next); err != nil {
		return err
	}
	err = a.restart(ctx)
	if err == nil {
		return nil
	}
	msg := err.Error()
	if tail := a.logTail(ctx); tail != "" {
		msg += "\n" + tail
	}
	if hadPrev {
		err = writeFile(a.Path, prev)
	} else {
		err = os.Remove(a.Path)
	}
	if err == nil {
		err = a.restart(ctx)
	}
	if err != nil {
		return fmt.Errorf("%s\nrolling back also failed, so the gateway may be down: %v", msg, err)
	}
	return fmt.Errorf("%s\nrolled back to the previous config", msg)
}

func (a *LocalApplier) restart(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "sh", "-c", a.Restart).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restarting the gateway failed (%v): %s", err, strings.TrimSpace(string(out)))
	}
	if a.Ready != nil {
		return a.Ready(ctx)
	}
	return nil
}

func (a *LocalApplier) logTail(ctx context.Context) string {
	if a.Log == "" {
		return ""
	}
	out, _ := exec.CommandContext(ctx, "sh", "-c", a.Log).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// writeFile replaces path in one rename, so aigw never reads half a file.
func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// HTTPReady waits for the gateway at url to answer anything at all (the key
// check refuses an unkeyed request, which is an answer).
func HTTPReady(url string, timeout time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var last error
		for {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				res.Body.Close()
				return nil
			}
			last = err
			select {
			case <-ctx.Done():
				return fmt.Errorf("the gateway didn't answer at %s within %s: %v", url, timeout, last)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}
