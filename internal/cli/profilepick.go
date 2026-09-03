// The start picker's profile face: one arrow-key list, last-used first so
// enter repeats yesterday's target. Returns "" for "other…" (raw picker).
package cli

import (
	"fmt"
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

type profilePickModel struct {
	names   []string // display order; "" sentinel = other…
	labels  []string
	cursor  int
	chosen  string
	aborted bool
	done    bool
}

// profileOrder lists profile names with the last-used first — enter-enter
// repeats yesterday's choice.
func profileOrder(c *pinnedConfig) []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	if i := slices.Index(names, c.LastProfile); i > 0 {
		names = append([]string{c.LastProfile}, slices.Delete(names, i, i+1)...)
	}
	return names
}

func pickProfile(c *pinnedConfig) (string, error) {
	names := profileOrder(c)
	m := profilePickModel{}
	for _, name := range names {
		p := c.Profiles[name]
		m.names = append(m.names, name)
		m.labels = append(m.labels, fmt.Sprintf("%-12s %s/%s", name, p.Context, p.Namespace))
	}
	m.names = append(m.names, "")
	m.labels = append(m.labels, "other cluster / namespace…")

	out, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	final, ok := out.(profilePickModel)
	if !ok {
		return "", fmt.Errorf("picker returned unexpected model %T", out)
	}
	if final.aborted {
		return "", fmt.Errorf("no target chosen — rerun and pick, or pass -p <profile>")
	}
	return final.chosen, nil
}

func (m profilePickModel) Init() tea.Cmd { return nil }

func (m profilePickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.names)-1 {
			m.cursor++
		}
	case "enter":
		m.chosen = m.names[m.cursor]
		m.done = true
		return m, tea.Quit
	case "q", "esc", "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	}
	return m, nil
}

func (m profilePickModel) View() string {
	if m.done || m.aborted {
		return ""
	}
	s := styleBrand.Render("◇ tiny") + styleSubtle.Render("  which fleet? · enter repeats last · -p skips") + "\n\n"
	for i, label := range m.labels {
		cur := "  "
		if i == m.cursor {
			cur = styleOK.Render("▸ ")
		}
		s += "  " + cur + label + "\n"
	}
	return s + styleSubtle.Render("  enter picks · esc quits") + "\n"
}
