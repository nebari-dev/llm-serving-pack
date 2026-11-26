package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testValues = `usage:
  collector:
    image:
      repository: old/repo
      # v1, pinned by digest.
      tag: "v1"
`

func newTree(t *testing.T, values string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"versions.env": "V=v9\nIMG=new/repo:v2@sha256:abc\n",
		"t/a.tmpl":     "v: {{.V}}\n",
		"values.yaml":  values,
	}
	for p, c := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, p := range []string{"ex/a.yaml", "values.yaml"} {
		if b, err := os.ReadFile(filepath.Join(root, p)); err == nil {
			got[p] = string(b)
		}
	}
	return got
}

func TestRunMain(t *testing.T) {
	outs := []Output{{Template: "t/a.tmpl", Dest: "ex/a.yaml"}}
	imgs := []ImageField{{File: "values.yaml", Path: []string{"usage", "collector", "image"}, Key: "IMG"}}
	cli := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := runMain(args, &o, &e, outs, imgs)
		return code, o.String(), e.String()
	}

	usage := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"missing root", nil, 2},
		{"unknown flag", []string{"--bogus"}, 2},
		{"check without root", []string{"--check"}, 2},
		{"flag missing value", []string{"--root"}, 2},
	}
	for _, tt := range usage {
		t.Run(tt.name, func(t *testing.T) {
			if code, _, _ := cli(tt.args...); code != tt.wantCode {
				t.Fatalf("code = %d, want %d", code, tt.wantCode)
			}
		})
	}

	// The steps run in order against one tree: each step starts from the
	// state the previous one left.
	root := newTree(t, testValues)
	steps := []struct {
		name         string
		args         []string
		wantCode     int
		wantContains []string
		wantEmptyOut bool
		wantChanged  bool
	}{
		{"stale check", []string{"--root", root, "--check"}, 1, []string{"ex/a.yaml", "values.yaml"}, false, false},
		{"write", []string{"--root", root}, 0, nil, false, true},
		{"fresh check", []string{"--root", root, "--check"}, 0, nil, true, false},
	}
	for _, st := range steps {
		before := readTree(t, root)
		code, out, errOut := cli(st.args...)
		if code != st.wantCode {
			t.Fatalf("%s: code = %d (%s), want %d", st.name, code, errOut, st.wantCode)
		}
		for _, want := range st.wantContains {
			if !strings.Contains(out, want) {
				t.Fatalf("%s: output %q does not list %s", st.name, out, want)
			}
		}
		if st.wantEmptyOut && out != "" {
			t.Fatalf("%s: output = %q, want empty", st.name, out)
		}
		after := readTree(t, root)
		if changed := !equalMaps(before, after); changed != st.wantChanged {
			t.Fatalf("%s: tree changed = %v, want %v", st.name, changed, st.wantChanged)
		}
		if st.name == "write" {
			if after["ex/a.yaml"] != "# Generated from t/a.tmpl, do not edit.\nv: v9\n" {
				t.Fatalf("ex/a.yaml = %q", after["ex/a.yaml"])
			}
			if !strings.Contains(after["values.yaml"], `tag: "v2@sha256:abc"`) {
				t.Fatalf("values.yaml = %q", after["values.yaml"])
			}
		}
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
