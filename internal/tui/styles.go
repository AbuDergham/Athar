package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Styles use the 16 ANSI colours so they follow the terminal's theme.
var (
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleKey      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleRule     = lipgloss.NewStyle().Faint(true)
	styleCursor   = lipgloss.NewStyle().Reverse(true)
)

// fit truncates s (ANSI-aware) to width cells and pads it with spaces to
// exactly width cells.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if w := cellWidth(s); w > width {
		s = ansi.Truncate(s, width, "…")
	}
	if pad := width - cellWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// spread puts left and right on one line of width cells, right-aligned right
// (left is truncated first when both do not fit).
func spread(left, right string, width int) string {
	rw := cellWidth(right)
	if rw >= width {
		return fit(right, width)
	}
	return fit(left, width-rw) + right
}

// keyHelp renders "key label" pairs for the footer.
func keyHelp(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(styleKey.Render(pairs[i]))
		b.WriteString(" ")
		b.WriteString(pairs[i+1])
	}
	return b.String()
}
