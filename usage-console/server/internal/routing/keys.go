package routing

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// KeyStore holds provider keys where the gateway reads them (spec §9.1).
// Postgres keeps only a reference (Ref), the key's prefix and its last test;
// the key itself goes here and nowhere else. The local one writes an env file
// aigw is started with; a Kubernetes one would write the Secret the
// BackendSecurityPolicy names.
type KeyStore interface {
	// Target says where keys go, for the console.
	Target() string
	// Put stores key under ref, replacing any key there.
	Put(ref, key string) error
	// Get is the key under ref, for testing a saved backend's connection.
	Get(ref string) (key string, ok bool, err error)
	// Remove deletes the key under ref, if any.
	Remove(ref string) error
}

// KeyRef is the reference a backend's key is stored under: an environment
// variable name, which the compiled Secret substitutes.
func KeyRef(backend string) string {
	var b strings.Builder
	b.WriteString("STARGATE_PROVIDER_KEY_")
	for _, r := range strings.ToUpper(backend) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// KeyPrefix is the part of a key the console may show and the audit log may
// name: at most 8 characters and a third of the key.
func KeyPrefix(key string) string {
	return key[:min(8, len(key)/3)]
}

// ValidateKey refuses what can't be a provider key, and anything that would
// break the line it's written on.
func ValidateKey(key string) error {
	switch {
	case len(key) < 8:
		return errors.New("that's too short to be a provider key")
	case len(key) > 4096:
		return errors.New("that's too long to be a provider key")
	case strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return errors.New("a provider key can't contain spaces or line breaks")
	}
	return nil
}

var refRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// LocalKeyFile keeps keys as KEY=VALUE lines in Path, owner-only (0600),
// values taken literally: the format of `docker run --env-file`, and what
// scripts/restart.sh loads before starting aigw. Fallback files (server/.env)
// are read, never written, for keys set there by hand.
type LocalKeyFile struct {
	Path     string
	Fallback []string
	mu       sync.Mutex
}

func (f *LocalKeyFile) Target() string { return "the gateway's key file " + f.Path }

// envLine is one line of an env file: a KEY=VALUE pair, or anything else
// (comments, blanks), kept as it was.
type envLine struct{ key, value, raw string }

func readEnv(path string, lenient bool) ([]envLine, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []envLine
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := sc.Text()
		line := raw
		if lenient {
			line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(k), "#") {
			out = append(out, envLine{raw: raw})
			continue
		}
		if lenient && len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out = append(out, envLine{key: strings.TrimSpace(k), value: v, raw: raw})
	}
	return out, sc.Err()
}

func (f *LocalKeyFile) Get(ref string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, path := range append([]string{f.Path}, f.Fallback...) {
		lines, err := readEnv(path, i > 0)
		if err != nil {
			return "", false, err
		}
		for _, l := range lines {
			if l.key == ref && l.value != "" {
				return l.value, true, nil
			}
		}
	}
	return "", false, nil
}

func (f *LocalKeyFile) Put(ref, key string) error {
	if !refRE.MatchString(ref) {
		return fmt.Errorf("%q isn't an environment variable name", ref)
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	return f.update(ref, &key)
}

func (f *LocalKeyFile) Remove(ref string) error { return f.update(ref, nil) }

// update sets ref to *key, or removes it when key is nil, and rewrites the
// file in one rename.
func (f *LocalKeyFile) update(ref string, key *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := readEnv(f.Path, false)
	if err != nil {
		return err
	}
	var b strings.Builder
	done := false
	for _, l := range lines {
		switch {
		case l.key != ref:
			b.WriteString(l.raw + "\n")
		case key != nil && !done:
			b.WriteString(ref + "=" + *key + "\n")
			done = true
		}
	}
	if key != nil && !done {
		b.WriteString(ref + "=" + *key + "\n")
	}
	return writeSecretFile(f.Path, []byte(b.String()))
}

// writeSecretFile replaces path in one rename with an owner-only file. The
// temporary file is created 0600, so the key is never readable by others,
// even for a moment.
func writeSecretFile(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".provider-keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
