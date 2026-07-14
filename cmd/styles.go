package cmd

import "github.com/charmbracelet/lipgloss"

// Brand palette — indigo, matching the Tiny Systems web surfaces so the
// terminal and the browser editor read as one product.
var (
	indigo = lipgloss.Color("#6366f1")
	green  = lipgloss.Color("#10b981")
	amber  = lipgloss.Color("#f59e0b")
	subtle = lipgloss.Color("#6b7280")

	styleLogo   = lipgloss.NewStyle().Bold(true).Foreground(indigo)
	styleTitle  = lipgloss.NewStyle().Bold(true)
	styleSubtle = lipgloss.NewStyle().Foreground(subtle)
	styleKey    = lipgloss.NewStyle().Foreground(indigo)
	styleOK     = lipgloss.NewStyle().Foreground(green)
	styleWarn   = lipgloss.NewStyle().Foreground(amber)
	styleURL    = lipgloss.NewStyle().Foreground(indigo).Underline(true)

	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(indigo).
			Padding(0, 2)
)

// banner is the little wordmark shown at the top of interactive output.
func banner() string {
	return styleLogo.Render("◇ tiny") + styleSubtle.Render("  self-hosted AI agents on your own Kubernetes")
}
