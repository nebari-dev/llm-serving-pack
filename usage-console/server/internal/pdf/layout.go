package pdf

import (
	"strings"
	"time"
)

// Margin is the page margin on every side, in points.
const Margin = 54.0

// Layout flows headings, paragraphs and tables down pages, starting a new
// page when the next line won't fit above the footer.
type Layout struct {
	*Doc
	y float64
}

func NewLayout(title string) *Layout {
	l := &Layout{Doc: New(title)}
	l.newPage()
	return l
}

func (l *Layout) newPage() {
	l.AddPage()
	l.y = PageH - Margin
}

// bottom is the lowest baseline above the footer.
const bottom = Margin + 10

// room starts a new page unless h points fit, and reports whether it did.
func (l *Layout) room(h float64) bool {
	if l.y-h < bottom {
		l.newPage()
		return true
	}
	return false
}

// Space moves down h points.
func (l *Layout) Space(h float64) { l.y -= h }

// Title is the document's first line.
func (l *Layout) Title(s string) {
	l.room(24)
	l.y -= 18
	l.Text(Margin, l.y, 16, true, s)
	l.y -= 8
}

// Heading starts a section, with a rule under it.
func (l *Layout) Heading(s string) {
	l.room(40) // keep a heading with at least a line of what follows
	l.y -= 18
	l.Text(Margin, l.y, 11, true, s)
	l.y -= 5
	l.Line(Margin, l.y, PageW-Margin, l.y, 0.6, 0.55)
	l.y -= 4
}

// Para wraps s to the text width.
func (l *Layout) Para(s string, size float64) { l.para(s, size, false) }

// Bold is Para in bold.
func (l *Layout) Bold(s string, size float64) { l.para(s, size, true) }

func (l *Layout) para(s string, size float64, bold bool) {
	for _, line := range wrap(s, size, PageW-2*Margin, bold) {
		l.room(size * 1.4)
		l.y -= size * 1.4
		l.Text(Margin, l.y, size, bold, line)
	}
	l.y -= size * 0.4
}

// Pair is a label and its value on one line, the value in bold.
func (l *Layout) Pair(label, value string, size float64) {
	l.room(size * 1.5)
	l.y -= size * 1.5
	l.Text(Margin, l.y, size, false, label)
	l.Text(Margin+150, l.y, size, true, value)
}

func wrap(s string, size, max float64, bold bool) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		next := w
		if line != "" {
			next = line + " " + w
		}
		if line != "" && Width(next, size, bold) > max {
			lines = append(lines, line)
			next = w
		}
		line = next
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// Col is a table column. Widths are in points; Right aligns numbers.
type Col struct {
	Title string
	Width float64
	Right bool
}

const tableSize = 8.5

func (l *Layout) row(cols []Col, cells []string, bold bool) {
	l.y -= tableSize * 1.6
	x := Margin
	for i, c := range cols {
		if i < len(cells) {
			s := Fit(cells[i], tableSize, c.Width-6, bold)
			if c.Right {
				l.TextRight(x+c.Width-2, l.y, tableSize, bold, s)
			} else {
				l.Text(x+2, l.y, tableSize, bold, s)
			}
		}
		x += c.Width
	}
}

func (l *Layout) header(cols []Col) {
	titles := make([]string, len(cols))
	for i, c := range cols {
		titles[i] = c.Title
	}
	l.row(cols, titles, true)
	l.y -= 3
	l.Line(Margin, l.y, Margin+tableWidth(cols), l.y, 0.4, 0.7)
}

func tableWidth(cols []Col) float64 {
	w := 0.0
	for _, c := range cols {
		w += c.Width
	}
	return w
}

// Table draws a header, the rows, and total (if any) under a rule. A table
// running onto a new page repeats its header there.
func (l *Layout) Table(cols []Col, rows [][]string, total []string) {
	l.room(tableSize * 1.6 * 3)
	l.header(cols)
	for _, r := range rows {
		if l.room(tableSize * 1.6) {
			l.header(cols)
		}
		l.row(cols, r, false)
	}
	if total != nil {
		if l.room(tableSize*1.6 + 4) {
			l.header(cols)
		}
		l.y -= 3
		l.Line(Margin, l.y, Margin+tableWidth(cols), l.y, 0.4, 0.7)
		l.row(cols, total, true)
	}
	l.y -= 4
}

// Bytes is the finished file (Doc.Bytes).
func (l *Layout) Bytes(created time.Time, footer string) []byte { return l.Doc.Bytes(created, footer) }
