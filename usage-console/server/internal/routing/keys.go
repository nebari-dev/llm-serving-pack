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

// KeyStore holds provider keys for the gateway (spec §9.1). Postgres keeps
// only a reference (Ref), the key's prefix and its last test; the key itself
// goes here and nowhere else. A key put or removed is staged: the gateway
// reads it only once routing is applied (§7.1 principle 4), when the
// applier promotes what's staged. The local one writes env files aigw is
// started with; a Kubernetes one would write the Secret the
// BackendSecurityPolicy names.
type KeyStore interface {
	// Target says where keys go, for the console.
	Target() string
	// Put stages key under ref, replacing any key there.
	Put(ref, key string) error
	// Get is the key under ref as staged (else as applied), for testing a
	// saved backend's connection.
	Get(ref string) (key string, ok bool, err error)
	// Remove stages removing the key under ref, if any.
	Remove(ref string) error
}

// KeyPromoter makes staged keys the ones the gateway reads. An apply calls
// Promote with the config it writes, and undo if the gateway doesn't come
// back, so keys and config roll back together.
type KeyPromoter interface {
	Promote() (undo func() error, err error)
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
// scripts/restart.sh loads before starting aigw. Put and Remove write the
// staged file instead (StagedPath: Path's whole next version, also 0600),
// which nothing starts aigw with; Promote moves it over Path. Fallback files
// (server/.env) are read, never written, for keys set there by hand.
type LocalKeyFile struct {
	Path     string
	Fallback []string
	mu       sync.Mutex
}

func (f *LocalKeyFile) Target() string {
	return "the gateway's key file " + f.Path + " (staged in " + f.StagedPath() + " until routing is applied)"
}

// StagedPath is where keys wait for an apply: provider-keys.env's staged
// file is provider-keys.pending.env.
func (f *LocalKeyFile) StagedPath() string {
	ext := filepath.Ext(f.Path)
	return strings.TrimSuffix(f.Path, ext) + ".pending" + ext
}

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

// next is the staged file if there is one, else the live one: what Path
// would hold after the next promote.
func (f *LocalKeyFile) next() ([]envLine, error) {
	lines, err := readEnv(f.StagedPath(), false)
	if err != nil || lines != nil {
		return lines, err
	}
	if _, err := os.Stat(f.StagedPath()); err == nil {
		return nil, nil // staged, and empty: every key removed
	}
	return readEnv(f.Path, false)
}

func (f *LocalKeyFile) Get(ref string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := f.next()
	if err != nil {
		return "", false, err
	}
	for _, l := range lines {
		if l.key == ref && l.value != "" {
			return l.value, true, nil
		}
	}
	for _, path := range f.Fallback {
		lines, err := readEnv(path, true)
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

// update stages ref set to *key, or removed when key is nil, rewriting the
// staged file in one rename.
func (f *LocalKeyFile) update(ref string, key *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := f.next()
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
	return writeSecretFile(f.StagedPath(), []byte(b.String()))
}

// Promote moves the staged file over the live one. undo puts the live file
// back as it was and the promoted keys back in staging, unless a key was
// staged since (that staged file already holds them).
func (f *LocalKeyFile) Promote() (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	staged, err := os.ReadFile(f.StagedPath())
	if errors.Is(err, fs.ErrNotExist) {
		return func() error { return nil }, nil
	}
	if err != nil {
		return nil, err
	}
	prev, err := os.ReadFile(f.Path)
	hadPrev := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := writeSecretFile(f.Path, staged); err != nil {
		return nil, err
	}
	if err := os.Remove(f.StagedPath()); err != nil {
		return nil, err
	}
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, err := os.Stat(f.StagedPath()); errors.Is(err, fs.ErrNotExist) {
			if err := writeSecretFile(f.StagedPath(), staged); err != nil {
				return err
			}
		}
		if !hadPrev {
			return os.Remove(f.Path)
		}
		return writeSecretFile(f.Path, prev)
	}, nil
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
