// Package theme holds the lipgloss styles every screen renders with, so a
// colour is defined once, plus the small text helpers (Pad) the list
// screens share.
package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The shared styles. Bad is bold red for failures and attention; Warn is
// yellow; OK green; Info blue; Dim faint; Sel is the reverse-video cursor row.
var (
	Bold      = lipgloss.NewStyle().Bold(true)
	Dim       = lipgloss.NewStyle().Faint(true)
	Warn      = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	Bad       = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	OK        = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	Info      = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	Sel       = lipgloss.NewStyle().Reverse(true)
	Tab       = lipgloss.NewStyle().Faint(true)
	TabActive = lipgloss.NewStyle().Reverse(true).Bold(true)
	Modal     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

// Pad fits s into a column of n cells: longer text is cut with an ellipsis
// (leaving one cell of gap), shorter text is padded with spaces.
func Pad(s string, n int) string {
	s = ansi.Truncate(s, n-1, "…")
	return s + strings.Repeat(" ", max(n-ansi.StringWidth(s), 0))
}
