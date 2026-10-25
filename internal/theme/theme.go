// Package theme holds liki's colors and text styles. Blue is the accent.
// Styles render as plain text when output is not a color terminal.
package theme

import "github.com/charmbracelet/lipgloss"

var (
	Blue      = lipgloss.Color("#3B82F6")
	LightBlue = lipgloss.Color("#60A5FA")
	PaleBlue  = lipgloss.Color("#93C5FD")
	Muted     = lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}
	Faint     = lipgloss.AdaptiveColor{Light: "#CBD5E1", Dark: "#475569"}
	Red       = lipgloss.Color("#F87171")
)

var (
	Accent = lipgloss.NewStyle().Foreground(Blue)
	Strong = lipgloss.NewStyle().Foreground(LightBlue).Bold(true)
	Dim    = lipgloss.NewStyle().Foreground(Muted)
	Weak   = lipgloss.NewStyle().Foreground(Faint)
	Error  = lipgloss.NewStyle().Foreground(Red)
	Box    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Blue).Padding(0, 1)
)
