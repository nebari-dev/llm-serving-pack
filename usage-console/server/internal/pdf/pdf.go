// Package pdf writes small text-and-rule PDF documents: enough for a report
// a finance team files, without a dependency. It uses the standard Helvetica
// fonts (nothing embedded), WinAnsi encoding, and uncompressed content
// streams, so the text in a file can be searched as is.
//
// Characters outside WinAnsi (Latin-1 plus a few typographic marks) print
// as "?": the standard fonts can't draw them, and embedding a Unicode font
// would mean shipping and subsetting one.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// US Letter, in points.
const (
	PageW = 612.0
	PageH = 792.0
)

// Doc is a document being written: pages of drawing operators.
type Doc struct {
	title string
	pages []*bytes.Buffer
}

func New(title string) *Doc { return &Doc{title: title} }

// AddPage starts a new page; drawing goes to the last page.
func (d *Doc) AddPage() { d.pages = append(d.pages, &bytes.Buffer{}) }

// Pages is how many pages there are so far.
func (d *Doc) Pages() int { return len(d.pages) }

func (d *Doc) page() *bytes.Buffer {
	if len(d.pages) == 0 {
		d.AddPage()
	}
	return d.pages[len(d.pages)-1]
}

func font(bold bool) string {
	if bold {
		return "F2"
	}
	return "F1"
}

// Text draws s with its baseline starting at (x, y), from the bottom left.
func (d *Doc) Text(x, y, size float64, bold bool, s string) {
	fmt.Fprintf(d.page(), "BT /%s %s Tf %s %s Td (%s) Tj ET\n", font(bold), num(size), num(x), num(y), literal(s))
}

// TextRight draws s ending at x.
func (d *Doc) TextRight(x, y, size float64, bold bool, s string) {
	d.Text(x-Width(s, size, bold), y, size, bold, s)
}

// Line draws a rule of the given width and gray (0 black, 1 white).
func (d *Doc) Line(x1, y1, x2, y2, width, gray float64) {
	fmt.Fprintf(d.page(), "q %s G %s w %s %s m %s %s l S Q\n", num(gray), num(width), num(x1), num(y1), num(x2), num(y2))
}

// Bytes is the finished file. Each page gets footer (if any) at its bottom
// left and "Page i of n" at its bottom right.
func (d *Doc) Bytes(created time.Time, footer string) []byte {
	if len(d.pages) == 0 {
		d.AddPage()
	}
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	n := len(d.pages)
	// 1 catalog, 2 page tree, 3 and 4 fonts, 5 info, then a page and its
	// content per page.
	kids := make([]string, n)
	for i := range kids {
		kids[i] = fmt.Sprintf("%d 0 R", 6+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	obj(fmt.Sprintf("<< /Title (%s) /Producer (Stargate) /CreationDate (D:%s) >>", literal(d.title), created.UTC().Format("20060102150405Z")))
	for i, p := range d.pages {
		content := bytes.NewBuffer(append([]byte(nil), p.Bytes()...))
		foot := &Doc{pages: []*bytes.Buffer{content}}
		if footer != "" {
			foot.Text(Margin, 30, 7.5, false, Fit(footer, 7.5, PageW-2*Margin-80, false))
		}
		foot.TextRight(PageW-Margin, 30, 7.5, false, fmt.Sprintf("Page %d of %d", i+1, n))
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>",
			num(PageW), num(PageH), 7+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.Bytes()))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Info 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

// num formats a coordinate without float noise.
func num(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

// literal is s as the body of a PDF string: WinAnsi bytes, with \ ( )
// escaped.
func literal(s string) string {
	var b strings.Builder
	for _, c := range encode(s) {
		if c == '\\' || c == '(' || c == ')' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// winAnsi maps the typographic marks WinAnsi has above 0x7f outside
// Latin-1's range.
var winAnsi = map[rune]byte{
	'€': 0x80, '‚': 0x82, '„': 0x84, '…': 0x85, '‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94,
	'•': 0x95, '–': 0x96, '—': 0x97, '™': 0x99,
}

// encode is s in WinAnsi. Control characters become spaces; "→" becomes
// "->"; anything else the fonts lack becomes "?".
func encode(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r == '→':
			out = append(out, '-', '>')
		case r < 0x20 || r == 0x7f:
			out = append(out, ' ')
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xa0 && r <= 0xff:
			out = append(out, byte(r))
		case winAnsi[r] != 0:
			out = append(out, winAnsi[r])
		default:
			out = append(out, '?')
		}
	}
	return out
}

// Advance widths in 1/1000 em for 0x20–0x7e, from Adobe's Helvetica and
// Helvetica-Bold AFM files.
var (
	regular = [95]int{
		278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
		556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
		1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
		667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
		333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
		556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
	}
	bold = [95]int{
		278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
		556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
		975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
		667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
		333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
		611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584,
	}
	// high are the widths above 0x7f that aren't the default 556 (both fonts).
	high = map[byte]int{0x85: 1000, 0x91: 222, 0x92: 222, 0x95: 350, 0x96: 556, 0x97: 1000, 0x99: 1000, 0xa0: 278, 0xb7: 278, 0xd7: 584}
)

// Width is how wide s is at size, in points.
func Width(s string, size float64, isBold bool) float64 {
	table := &regular
	if isBold {
		table = &bold
	}
	sum := 0
	for _, c := range encode(s) {
		switch {
		case c >= 0x20 && c < 0x7f:
			sum += table[c-0x20]
		case high[c] != 0:
			sum += high[c]
		default:
			sum += 556
		}
	}
	return float64(sum) * size / 1000
}

// Fit is s, cut to fit max points with "..." when it doesn't.
func Fit(s string, size, max float64, isBold bool) string {
	if Width(s, size, isBold) <= max {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && Width(string(r)+"...", size, isBold) > max {
		r = r[:len(r)-1]
	}
	return string(r) + "..."
}
