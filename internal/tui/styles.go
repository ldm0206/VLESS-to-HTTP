package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// styles bundles every style the console uses. With NO_COLOR set the colored
// variants fall back to plain attributes so the console stays readable in a
// dumb terminal or when the output is piped somewhere.
type styles struct {
	plain bool

	title    lipgloss.Style
	tab      lipgloss.Style
	tabOn    lipgloss.Style
	section  lipgloss.Style
	selected lipgloss.Style
	dim      lipgloss.Style
	good     lipgloss.Style
	warn     lipgloss.Style
	bad      lipgloss.Style
	accent   lipgloss.Style
	footer   lipgloss.Style
	box      lipgloss.Style
}

func newStyles(noColor bool) styles {
	s := styles{plain: noColor}
	color := func(name string) lipgloss.TerminalColor {
		if noColor {
			return lipgloss.NoColor{}
		}
		return lipgloss.Color(name)
	}

	s.title = lipgloss.NewStyle().Bold(true).Foreground(color("63"))
	s.tab = lipgloss.NewStyle().Foreground(color("245"))
	s.tabOn = lipgloss.NewStyle().Bold(true).Foreground(color("231")).Background(color("62"))
	s.section = lipgloss.NewStyle().Bold(true).Foreground(color("110"))
	s.selected = lipgloss.NewStyle().Reverse(true)
	s.dim = lipgloss.NewStyle().Foreground(color("242"))
	s.good = lipgloss.NewStyle().Foreground(color("42"))
	s.warn = lipgloss.NewStyle().Foreground(color("214"))
	s.bad = lipgloss.NewStyle().Foreground(color("203"))
	s.accent = lipgloss.NewStyle().Foreground(color("39"))
	s.footer = lipgloss.NewStyle().Foreground(color("242"))
	s.box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color("62")).
		Padding(0, 1)
	return s
}

// gray renders text for nodes and subscriptions the core cannot use.
func (s styles) gray(text string) string { return s.dim.Render(text) }

// --- text helpers ---------------------------------------------------------

// truncate cuts text to at most w display columns, appending an ellipsis when
// something was dropped. Only plain, unstyled text may be passed in.
func truncate(text string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= w {
		return text
	}
	if w == 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		rw := lipgloss.Width(string(r))
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "…"
}

// pad right-pads plain text to exactly w display columns.
func pad(text string, w int) string {
	text = truncate(text, w)
	if gap := w - lipgloss.Width(text); gap > 0 {
		text += strings.Repeat(" ", gap)
	}
	return text
}

// fitTop keeps at most n lines, dropping the tail.
func fitTop(lines []string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// --- tables ---------------------------------------------------------------

// tcol is one table column. prio decides what survives a narrow terminal:
// 3 is never dropped, 0 is dropped first. flex marks the column that gets
// truncated when even the essential columns do not fit.
type tcol struct {
	title string
	prio  int
	flex  bool
}

// renderTable lays out a plain-text table. Cells must not contain escape
// sequences; callers style whole lines afterwards.
func renderTable(cols []tcol, rows [][]string, width int) []string {
	if len(cols) == 0 {
		return nil
	}
	if width < 12 {
		width = 12
	}

	keep := make([]bool, len(cols))
	for i := range keep {
		keep[i] = true
	}

	// Drop the least important columns until the table fits.
	for guard := 0; guard < len(cols); guard++ {
		widths := columnWidths(cols, rows, keep)
		if totalWidth(widths) <= width {
			break
		}
		drop := -1
		for i := range cols {
			if !keep[i] || cols[i].prio >= 3 {
				continue
			}
			if drop == -1 || cols[i].prio < cols[drop].prio {
				drop = i
			}
		}
		if drop == -1 {
			break
		}
		keep[drop] = false
	}

	widths := columnWidths(cols, rows, keep)

	// Still too wide: shave the flexible column down.
	for guard := 0; guard < 64; guard++ {
		if totalWidth(widths) <= width {
			break
		}
		idx := -1
		for i := range cols {
			if !keep[i] || !cols[i].flex {
				continue
			}
			if idx == -1 || widths[i] > widths[idx] {
				idx = i
			}
		}
		if idx == -1 {
			break
		}
		over := totalWidth(widths) - width
		if widths[idx]-over < 6 {
			over = widths[idx] - 6
		}
		if over <= 0 {
			break
		}
		widths[idx] -= over
	}

	out := make([]string, 0, len(rows)+1)
	header := make([]string, 0, len(cols))
	for i, c := range cols {
		if keep[i] {
			header = append(header, pad(c.title, widths[i]))
		}
	}
	out = append(out, strings.TrimRight(strings.Join(header, "  "), " "))

	for _, row := range rows {
		cells := make([]string, 0, len(cols))
		for i := range cols {
			if !keep[i] {
				continue
			}
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			cells = append(cells, pad(cell, widths[i]))
		}
		out = append(out, strings.TrimRight(strings.Join(cells, "  "), " "))
	}
	return out
}

func columnWidths(cols []tcol, rows [][]string, keep []bool) []int {
	widths := make([]int, len(cols))
	for i, c := range cols {
		if keep[i] {
			widths[i] = lipgloss.Width(c.title)
		}
	}
	for _, row := range rows {
		for i := range cols {
			if !keep[i] || i >= len(row) {
				continue
			}
			if w := lipgloss.Width(row[i]); w > widths[i] {
				widths[i] = w
			}
		}
	}
	return widths
}

func totalWidth(widths []int) int {
	total := 0
	visible := 0
	for _, w := range widths {
		if w <= 0 {
			continue
		}
		total += w
		visible++
	}
	if visible > 1 {
		total += 2 * (visible - 1)
	}
	return total
}
