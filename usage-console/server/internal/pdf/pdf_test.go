package pdf

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var created = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// xrefOK checks each xref entry points at its "n 0 obj", which is what a
// reader uses to find objects.
func xrefOK(t *testing.T, b []byte) {
	t.Helper()
	m := regexp.MustCompile(`startxref\n(\d+)\n%%EOF\n$`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("no startxref trailer:\n%s", b[max(0, len(b)-200):])
	}
	at, _ := strconv.Atoi(string(m[1]))
	if !bytes.HasPrefix(b[at:], []byte("xref\n0 ")) {
		t.Fatalf("startxref %d doesn't point at the xref table", at)
	}
	lines := strings.Split(string(b[at:]), "\n")
	n, _ := strconv.Atoi(strings.Fields(lines[1])[1])
	for i := 1; i < n; i++ {
		off, _ := strconv.Atoi(lines[2+i][:10])
		if want := fmt.Sprintf("%d 0 obj", i); !bytes.HasPrefix(b[off:], []byte(want)) {
			t.Errorf("xref entry %d at %d reads %q", i, off, b[off:off+12])
		}
	}
}

func TestWritesAValidDocument(t *testing.T) {
	d := New("Close report")
	d.AddPage()
	d.Text(72, 720, 12, true, "Spend (September 2026)")
	d.Line(72, 710, 540, 710, 0.5, 0.6)
	d.AddPage()
	d.Text(72, 720, 9, false, "second page")
	b := d.Bytes(created, "Footer text")

	if !bytes.HasPrefix(b, []byte("%PDF-1.4\n")) {
		t.Fatalf("header %q", b[:10])
	}
	xrefOK(t, b)
	if !bytes.Contains(b, []byte("/Count 2")) {
		t.Errorf("page count missing")
	}
	// Text stays uncompressed, so the content can be checked (and found).
	for _, s := range []string{"(Spend \\(September 2026\\)) Tj", "(second page) Tj", "(Page 1 of 2) Tj", "(Page 2 of 2) Tj", "(Footer text) Tj", "/Title (Close report)", "/BaseFont /Helvetica-Bold", "/Encoding /WinAnsiEncoding"} {
		if !bytes.Contains(b, []byte(s)) {
			t.Errorf("missing %q", s)
		}
	}
}

func TestEncodesWinAnsiAndEscapes(t *testing.T) {
	for in, want := range map[string]string{
		`a\b (c)`:       `a\\b \(c\)`,
		"Sep – Oct":     "Sep \x96 Oct",
		"a — b · c × d": "a \x97 b \xb7 c \xd7 d",
		"café":          "caf\xe9",
		"日本":            "??",
		"tab\there":     "tab here",
		"→":             "->",
	} {
		if got := literal(in); got != want {
			t.Errorf("literal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWidthAndFit(t *testing.T) {
	// Helvetica: "0" is 556/1000 em; bold "m" 889.
	if w := Width("00", 10, false); w != 11.12 {
		t.Errorf("Width(00) = %v", w)
	}
	if w := Width("m", 10, true); w != 8.89 {
		t.Errorf("bold Width(m) = %v", w)
	}
	long := strings.Repeat("abcdefghij", 10)
	got := Fit(long, 9, 100, false)
	if Width(got, 9, false) > 100 || !strings.HasSuffix(got, "...") {
		t.Errorf("Fit = %q (%v wide)", got, Width(got, 9, false))
	}
	if Fit("short", 9, 100, false) != "short" {
		t.Errorf("Fit changed a string that fits")
	}
}

func TestLayoutBreaksPagesAndRepeatsTableHeaders(t *testing.T) {
	l := NewLayout("Report")
	l.Heading("By key")
	cols := []Col{{Title: "Key", Width: 300}, {Title: "Spend", Width: 100, Right: true}}
	var rows [][]string
	for i := range 120 {
		rows = append(rows, []string{fmt.Sprintf("key-%03d", i), "$1.00"})
	}
	l.Table(cols, rows, []string{"Total", "$120.00"})
	l.Para("After the table, wrapped onto several lines because it is long enough to need wrapping at this width and then some more words.", 9)
	b := l.Bytes(created, "")
	xrefOK(t, b)
	pages := l.Pages()
	if pages < 3 {
		t.Fatalf("120 rows on %d pages", pages)
	}
	if c := bytes.Count(b, []byte("(Key) Tj")); c != pages {
		t.Errorf("table header on %d of %d pages", c, pages)
	}
	for _, s := range []string{"(key-000) Tj", "(key-119) Tj", "(Total) Tj", "($120.00) Tj"} {
		if !bytes.Contains(b, []byte(s)) {
			t.Errorf("missing %q", s)
		}
	}
}
