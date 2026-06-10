package ui

import (
	"charm.land/lipgloss/v2" // Version is the vix version string rendered on the welcome screen. Set by
	"image/color"
	"strings"
)

// cmd/vix/main.go at startup from the ldflags-provided build version.
var Version = "dev" // renderVixBanner returns the VIX ASCII art with a vertical gradient
// from purple (top) to yellow (bottom).
func renderVixBanner() string {
	lines := []string{"██╗   ██╗ ██╗ ██╗  ██╗", "██║   ██║ ██║ ╚██╗██╔╝", "██║   ██║ ██║  ╚███╔╝ ", "╚██╗ ██╔╝ ██║  ██╔██╗ ", " ╚████╔╝  ██║ ██╔╝ ██╗", "  ╚═══╝   ╚═╝ ╚═╝  ╚═╝"} // Hand-picked vertical ramp: purple → pink → orange → gold → yellow → green.
	ramp := []color.Color{lipgloss.Color("#BC63FC"),
		// purple
		lipgloss.Color("#F04EAA"),
		// pink
		lipgloss.Color("#FF7A55"),
		// orange
		lipgloss.Color("#FFAB22"),
		// gold
		lipgloss.Color("#FFD700"),
		// yellow
		lipgloss.Color("#39E831"),
		// green
	}
	var result strings.Builder
	for i, line := range lines {
		style := lipgloss.NewStyle().Foreground(ramp[i])
		result.WriteString(style.Render(line))
		result.WriteRune('\n')
	}
	return result.String()
} // renderWelcomeInline renders a centered welcome message for inline mode.
func renderWelcomeInline(width, height int, s Styles) string {
	// Build the welcome block (uncentered)
	var block strings.Builder
	block.WriteString(renderVixBanner())
	version := lipgloss.NewStyle().Foreground(s.ColorDimGray).Render(Version)
	block.WriteString(version + "\n\n")
	subtitle := lipgloss.NewStyle().Foreground(s.ColorWhite).Italic(true).Render("AI coding assistant")
	block.WriteString(subtitle + "\n\n")
	shortcutStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(s.ColorWhite)
	shortcuts := []struct {
		key  string
		desc string
	}{{"Tab", "Switch focus (input/chat)"}, {"Shift+Tab", "Cycle mode"}, {"Ctrl+N", "Next session"}, {"Ctrl+P", "Previous session"}, {"Ctrl+R", "Search history"}, {"Ctrl+C", "Quit"}, {"Esc", "Cancel current operation"}} // Find the longest key and longest desc to build fixed-width rows
	maxKeyWidth := 0
	maxDescWidth := 0
	for _, sc := range shortcuts {
		if len(sc.key) > maxKeyWidth {
			maxKeyWidth = len(sc.key)
		}
		if len(sc.desc) > maxDescWidth {
			maxDescWidth = len(sc.desc)
		}
	}
	rowWidth := maxKeyWidth + 2 + maxDescWidth // key + gap + desc
	for _, sc := range shortcuts {
		key := shortcutStyle.Width(maxKeyWidth).AlignHorizontal(lipgloss.Right).Render(sc.key)
		desc := descStyle.Width(maxDescWidth).Render(sc.desc)
		row := lipgloss.NewStyle().Width(rowWidth).Render(key + "  " + desc)
		block.WriteString(row + "\n")
	} // Center horizontally and vertically
	centered := lipgloss.NewStyle().Width(width).Height(height).AlignHorizontal(lipgloss.Center).AlignVertical(lipgloss.Center).Render(block.String())
	return centered
}

// renderRestoringInline renders a centered loading placeholder for a session
// that was attached on launch and is still waiting for its event.replay. It
// mirrors the welcome screen (vix banner + subtitle) but without version or
// shortcuts. spinner is the current animation frame (from ThinkingAnim.View);
// it may be empty, in which case only the banner and subtitle are shown.
func renderRestoringInline(width, height int, s Styles, spinner string) string {
	var block strings.Builder
	block.WriteString(renderVixBanner())
	block.WriteString("\n")
	subtitle := lipgloss.NewStyle().Foreground(s.ColorWhite).Italic(true).Render("loading sessions")
	block.WriteString(subtitle + "\n\n")
	if spinner != "" {
		block.WriteString(strings.TrimLeft(spinner, " "))
	}
	centered := lipgloss.NewStyle().Width(width).Height(height).AlignHorizontal(lipgloss.Center).AlignVertical(lipgloss.Center).Render(block.String())
	return centered
}
