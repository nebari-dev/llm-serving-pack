package routing

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestKeyRef(t *testing.T) {
	for in, want := range map[string]string{
		"together":      "STARGATE_PROVIDER_KEY_TOGETHER",
		"vllm-internal": "STARGATE_PROVIDER_KEY_VLLM_INTERNAL",
		"eu.openai-2":   "STARGATE_PROVIDER_KEY_EU_OPENAI_2",
	} {
		if got := KeyRef(in); got != want {
			t.Errorf("KeyRef(%q) = %q, want %q", in, got, want)
		}
	}
}

// The prefix identifies a key in the UI and the audit log without giving
// away enough of it to matter.
func TestKeyPrefixIsShort(t *testing.T) {
	for _, key := range []string{"sk-proj-0123456789abcdefghijklmnopqrstuvwxyz", "sk-or-v1-0123456789abcdef0123456789abcdef", "abcdefghijkl"} {
		p := KeyPrefix(key)
		if p == "" || len(p) > 8 || len(p)*3 > len(key) || !strings.HasPrefix(key, p) {
			t.Errorf("KeyPrefix(%q) = %q, want at most 8 chars and a third of the key", key, p)
		}
	}
}

func TestValidateKey(t *testing.T) {
	for _, bad := range []string{"", "short", "sk-has a space-0123456789", "sk-line\nbreak-0123456789", "sk-tab\t0123456789abc", strings.Repeat("k", 5000)} {
		if ValidateKey(bad) == nil {
			t.Errorf("ValidateKey(%q) accepted it", bad)
		}
	}
	for _, good := range []string{"sk-proj-0123456789abcdef", "sk-or-v1-abc=def/ghi+jkl", `quote"in$key-0123`} {
		if err := ValidateKey(good); err != nil {
			t.Errorf("ValidateKey(%q) = %v", good, err)
		}
	}
}

func readMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// The file is KEY=VALUE lines, values literal (docker --env-file's format),
// readable only by its owner, and replaced in one rename.
func TestLocalKeyFileWritesOwnerOnlyAndReplaces(t *testing.T) {
	dir := t.TempDir()
	f := &LocalKeyFile{Path: filepath.Join(dir, "aigw", "provider-keys.env")}
	if err := f.Put("STARGATE_PROVIDER_KEY_A", "sk-aaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if err := f.Put("STARGATE_PROVIDER_KEY_B", `sk-b$b"b=b`); err != nil {
		t.Fatal(err)
	}
	if err := f.Put("STARGATE_PROVIDER_KEY_A", "sk-a2a2a2a2a2a2a2a2"); err != nil {
		t.Fatal(err)
	}
	if m := readMode(t, f.Path); m != 0o600 {
		t.Errorf("mode %o, want 600", m)
	}
	b, _ := os.ReadFile(f.Path)
	text := string(b)
	for _, want := range []string{"STARGATE_PROVIDER_KEY_A=sk-a2a2a2a2a2a2a2a2\n", "STARGATE_PROVIDER_KEY_B=sk-b$b\"b=b\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("file lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sk-aaaa") {
		t.Errorf("the replaced key is still in the file:\n%s", text)
	}
	if got, ok, err := f.Get("STARGATE_PROVIDER_KEY_B"); err != nil || !ok || got != `sk-b$b"b=b` {
		t.Errorf("Get = %q, %v, %v", got, ok, err)
	}
	if err := f.Remove("STARGATE_PROVIDER_KEY_A"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.Get("STARGATE_PROVIDER_KEY_A"); ok {
		t.Errorf("removed key still there")
	}
	if entries, _ := os.ReadDir(filepath.Dir(f.Path)); len(entries) != 1 {
		t.Errorf("left temporary files behind: %v", entries)
	}
}

// A file someone created with looser permissions is tightened on the next write.
func TestLocalKeyFileTightensAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-keys.env")
	if err := os.WriteFile(path, []byte("# kept\nOTHER=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &LocalKeyFile{Path: path}
	if err := f.Put("STARGATE_PROVIDER_KEY_A", "sk-aaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if m := readMode(t, path); m != 0o600 {
		t.Errorf("mode %o, want 600", m)
	}
	if v, ok, _ := f.Get("OTHER"); !ok || v != "x" {
		t.Errorf("an entry the console didn't write was lost")
	}
}

func TestLocalKeyFileRefusesWhatWouldBreakTheFile(t *testing.T) {
	f := &LocalKeyFile{Path: filepath.Join(t.TempDir(), "k.env")}
	for _, ref := range []string{"", "lower_case", "A B", "A=B", "1ABC"} {
		if f.Put(ref, "sk-aaaaaaaaaaaaaaaa") == nil {
			t.Errorf("Put accepted ref %q", ref)
		}
	}
	if f.Put("STARGATE_PROVIDER_KEY_A", "sk-aaaa\nINJECTED=1") == nil {
		t.Errorf("Put accepted a key with a newline")
	}
	if _, err := os.Stat(f.Path); err == nil {
		b, _ := os.ReadFile(f.Path)
		if strings.Contains(string(b), "INJECTED") {
			t.Errorf("a refused key reached the file")
		}
	}
}

// Get falls back to read-only env files (server/.env) for keys set there by
// hand, and the console's file wins.
func TestLocalKeyFileFallback(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	_ = os.WriteFile(env, []byte("OPENROUTER_API_KEY=sk-or-from-dotenv\nexport QUOTED=\"x\"\n"), 0o600)
	f := &LocalKeyFile{Path: filepath.Join(dir, "keys.env"), Fallback: []string{env}}
	if v, ok, _ := f.Get("OPENROUTER_API_KEY"); !ok || v != "sk-or-from-dotenv" {
		t.Errorf("fallback Get = %q, %v", v, ok)
	}
	_ = f.Put("OPENROUTER_API_KEY", "sk-or-from-console")
	if v, _, _ := f.Get("OPENROUTER_API_KEY"); v != "sk-or-from-console" {
		t.Errorf("Get = %q, want the console's key", v)
	}
	if b, _ := os.ReadFile(env); strings.Contains(string(b), "console") {
		t.Errorf("wrote to the fallback file")
	}
}

func TestLocalKeyFileConcurrentPuts(t *testing.T) {
	f := &LocalKeyFile{Path: filepath.Join(t.TempDir(), "k.env")}
	var wg sync.WaitGroup
	for _, ref := range []string{"A", "B", "C", "D", "E", "F"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.Put("K_"+ref, "sk-"+strings.Repeat(ref, 16)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, ref := range []string{"A", "B", "C", "D", "E", "F"} {
		if _, ok, _ := f.Get("K_" + ref); !ok {
			t.Errorf("lost K_%s", ref)
		}
	}
}
