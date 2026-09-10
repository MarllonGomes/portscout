package ui

import "charm.land/lipgloss/v2"

// Styles is every colour the dashboard uses. It is a struct so a test can force
// everything to plain text and compare rendered output byte for byte.
type Styles struct {
	Title    lipgloss.Style
	Heading  lipgloss.Style
	Selected lipgloss.Style
	Up       lipgloss.Style
	Pending  lipgloss.Style
	Failed   lipgloss.Style
	Off      lipgloss.Style
	Absent   lipgloss.Style
	Info     lipgloss.Style
	Warn     lipgloss.Style
	Error    lipgloss.Style
	Hint     lipgloss.Style
	HintKey  lipgloss.Style
	Edit     lipgloss.Style
}

func DefaultStyles() Styles {
	return Styles{
		Title:    lipgloss.NewStyle().Bold(true),
		Heading:  lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		Selected: lipgloss.NewStyle().Reverse(true),
		Up:       lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		Pending:  lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		Failed:   lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		Off:      lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		Absent:   lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Info:     lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		Warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		Error:    lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		Hint:     lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		HintKey:  lipgloss.NewStyle().Foreground(lipgloss.Color("45")),
		Edit:     lipgloss.NewStyle().Reverse(true),
	}
}

// PlainStyles renders without any escape codes, for golden tests.
func PlainStyles() Styles {
	plain := lipgloss.NewStyle()
	return Styles{
		Title: plain, Heading: plain, Selected: plain, Up: plain, Pending: plain,
		Failed: plain, Off: plain, Absent: plain, Info: plain, Warn: plain,
		Error: plain, Hint: plain, HintKey: plain, Edit: plain,
	}
}
