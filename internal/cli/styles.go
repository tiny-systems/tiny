package cli

import "github.com/charmbracelet/lipgloss"

// The CLI's few voice styles — enough for upgrade output, nothing more.
var (
	styleTitle = lipgloss.NewStyle().Bold(true)
	// styleBrand is the ◇ tiny pill: spring green on deep green.
	styleBrand  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("84")).Background(lipgloss.Color("22")).Padding(0, 1)
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleSubtle = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	styleKey    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)
