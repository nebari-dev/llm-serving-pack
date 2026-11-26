package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"text/template"
)

// Pins maps a versions.env key to its value.
type Pins map[string]string

// Output is a published file rendered from a text/template source.
type Output struct {
	Template string // path of the template, relative to the root
	Dest     string // path of the published file, relative to the root
}

// ImageField is an image reference in a YAML file that a pin key writes.
type ImageField struct {
	File string   // path of the YAML file, relative to the root
	Path []string // keys leading to the image
	Key  string   // pin key holding repository:tag@digest
}

// ParsePins reads versions.env: one KEY=value per line, # comments, no
// quoting, no spaces around "=", no $ expansion, no duplicate keys.
func ParsePins(r io.Reader) (Pins, error) {
	pins := Pins{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		switch {
		case !ok:
			return nil, fmt.Errorf("line %d: want KEY=value, got %q", n, line)
		case key == "":
			return nil, fmt.Errorf("line %d: empty key", n)
		case key != strings.TrimSpace(key) || strings.ContainsAny(key, " \t"):
			return nil, fmt.Errorf("line %d: no spaces allowed in key %q", n, key)
		case value != strings.TrimSpace(value):
			return nil, fmt.Errorf("line %d: no spaces allowed around '=' for %s", n, key)
		case strings.ContainsAny(value, `"'`):
			return nil, fmt.Errorf("line %d: quoting is not allowed (%s)", n, key)
		case strings.Contains(value, "$"):
			return nil, fmt.Errorf("line %d: '$' expansion is not allowed (%s)", n, key)
		}
		if _, dup := pins[key]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %s", n, key)
		}
		pins[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return pins, nil
}

// Render executes the template with pins as data. A key the template names
// that pins lacks is an error. The output starts with a generated-file header.
func Render(tmpl io.Reader, name string, pins Pins) ([]byte, error) {
	src, err := io.ReadAll(tmpl)
	if err != nil {
		return nil, fmt.Errorf("reading template %s: %w", name, err)
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("parsing template %s: %w", name, err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Generated from %s, do not edit.\n", name)
	if err := t.Execute(&buf, map[string]string(pins)); err != nil {
		return nil, fmt.Errorf("rendering template %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// splitImage splits repository:tag[@digest] at the last ':' before '@'.
func splitImage(image string) (repo, tag string, err error) {
	ref, digest, hasDigest := strings.Cut(image, "@")
	i := strings.LastIndex(ref, ":")
	if i < 0 || i < strings.LastIndex(ref, "/") || i == 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("image %q has no tag", image)
	}
	repo, tag = ref[:i], ref[i+1:]
	if hasDigest {
		return repo, tag + "@" + digest, nil
	}
	return repo, tag, nil
}

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func isSkippable(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// findKey returns the line index of key among the children of the block
// lines[start:end], where children are indented deeper than parentIndent.
func findKey(lines []string, start, end, parentIndent int, key string) (int, bool) {
	childIndent := -1
	for i := start; i < end; i++ {
		if isSkippable(lines[i]) {
			continue
		}
		ind := indentOf(lines[i])
		if ind <= parentIndent {
			return 0, false
		}
		if childIndent < 0 {
			childIndent = ind
		}
		if ind == childIndent && strings.HasPrefix(lines[i][ind:], key+":") {
			return i, true
		}
	}
	return 0, false
}

// blockEnd returns the first line after i that is not nested under it.
func blockEnd(lines []string, i, end int) int {
	ind := indentOf(lines[i])
	for j := i + 1; j < end; j++ {
		if !isSkippable(lines[j]) && indentOf(lines[j]) <= ind {
			return j
		}
	}
	return end
}

// setValue replaces the value after "key:" on a line, keeping the indentation.
func setValue(line, key, value string) string {
	ind := indentOf(line)
	return line[:ind] + key + ": " + value
}

// SetImage writes image (repository:tag[@digest]) at path in a YAML file by
// editing lines, so every other byte stays identical. If the node at path is
// a mapping, only its repository and tag lines and the version comment
// directly above the tag change. If it is a scalar, only its value changes.
func SetImage(values []byte, path []string, image string) ([]byte, error) {
	repo, tag, err := splitImage(image)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(values), "\n")
	where := strings.Join(path, ".")
	start, end, parentIndent, idx := 0, len(lines), -1, -1
	for _, key := range path {
		i, ok := findKey(lines, start, end, parentIndent, key)
		if !ok {
			return nil, fmt.Errorf("path %s not found", where)
		}
		idx, parentIndent = i, indentOf(lines[i])
		start, end = i+1, blockEnd(lines, i, end)
	}
	if idx < 0 {
		return nil, fmt.Errorf("empty path")
	}
	if rest := strings.TrimSpace(strings.TrimPrefix(lines[idx][indentOf(lines[idx]):], path[len(path)-1]+":")); rest != "" {
		lines[idx] = setValue(lines[idx], path[len(path)-1], image)
		return []byte(strings.Join(lines, "\n")), nil
	}
	repoIdx, ok := findKey(lines, start, end, parentIndent, "repository")
	if !ok {
		return nil, fmt.Errorf("path %s: no repository key", where)
	}
	tagIdx, ok := findKey(lines, start, end, parentIndent, "tag")
	if !ok {
		return nil, fmt.Errorf("path %s: no tag key", where)
	}
	lines[repoIdx] = setValue(lines[repoIdx], "repository", repo)
	lines[tagIdx] = setValue(lines[tagIdx], "tag", `"`+tag+`"`)
	if c := tagIdx - 1; c > idx && strings.HasPrefix(strings.TrimSpace(lines[c]), "#") {
		version, _, _ := strings.Cut(tag, "@")
		lines[c] = lines[c][:indentOf(lines[c])] + "# " + version + ", pinned by digest."
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// Stale returns the destination paths, sorted, whose current content differs
// from what rendering would write. A missing destination is stale.
func Stale(fsys fs.FS, outputs []Output, images []ImageField, pins Pins) ([]string, error) {
	var stale []string
	for _, o := range outputs {
		want, err := renderOutput(fsys, o, pins)
		if err != nil {
			return nil, err
		}
		got, err := fs.ReadFile(fsys, o.Dest)
		if err != nil || !bytes.Equal(got, want) {
			stale = append(stale, o.Dest)
		}
	}
	for _, f := range images {
		want, err := renderImage(fsys, f, pins)
		if err != nil {
			return nil, err
		}
		got, err := fs.ReadFile(fsys, f.File)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(got, want) {
			stale = append(stale, f.File)
		}
	}
	sort.Strings(stale)
	return stale, nil
}

func renderOutput(fsys fs.FS, o Output, pins Pins) ([]byte, error) {
	f, err := fsys.Open(o.Template)
	if err != nil {
		return nil, fmt.Errorf("opening template: %w", err)
	}
	defer f.Close()
	return Render(f, o.Template, pins)
}

func renderImage(fsys fs.FS, f ImageField, pins Pins) ([]byte, error) {
	image, ok := pins[f.Key]
	if !ok {
		return nil, fmt.Errorf("%s: pin %s not found", f.File, f.Key)
	}
	cur, err := fs.ReadFile(fsys, f.File)
	if err != nil {
		return nil, err
	}
	out, err := SetImage(cur, f.Path, image)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.File, err)
	}
	return out, nil
}
