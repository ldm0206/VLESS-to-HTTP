package cli

import (
	"fmt"
	"io"
	"strings"
)

// table renders aligned columns; the width calculation counts CJK characters
// as two cells so Chinese node names line up.
type table struct {
	headers []string
	rows    [][]string
}

func newTable(headers ...string) *table {
	return &table{headers: headers}
}

func (t *table) add(cells ...string) {
	t.rows = append(t.rows, cells)
}

func (t *table) render(w io.Writer) {
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = displayWidth(h)
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if i >= len(widths) {
				break
			}
			if w := displayWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}

	writeRow := func(cells []string, bold bool) {
		var b strings.Builder
		for i, cell := range cells {
			if i >= len(widths) {
				break
			}
			b.WriteString(cell)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+2))
			}
		}
		line := strings.TrimRight(b.String(), " ")
		if bold {
			fmt.Fprintln(w, line)
		} else {
			fmt.Fprintln(w, line)
		}
	}

	writeRow(t.headers, true)
	separator := make([]string, len(t.headers))
	for i := range separator {
		separator[i] = strings.Repeat("-", widths[i])
	}
	writeRow(separator, false)
	for _, row := range t.rows {
		writeRow(row, false)
	}
}

// displayWidth approximates terminal columns for a string.
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		width += runeWidth(r)
	}
	return width
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32:
		return 0
	case r < 0x1100:
		return 1
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals … Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x20000 && r <= 0x3FFFD: // CJK extensions
		return 2
	default:
		return 1
	}
}

// padRight pads s to the given display width.
func padRight(s string, width int) string {
	pad := width - displayWidth(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}
