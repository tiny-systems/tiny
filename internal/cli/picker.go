package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	corev1 "k8s.io/api/core/v1"

	"github.com/tiny-systems/tiny/internal/kube"
)

// pickTarget is the first-run target chooser: arrow-key selection of the
// kubeconfig context, then of a namespace on that cluster — or a new one,
// typed. Returns what the human chose.
func pickTarget(current, defaultNS string) (string, string, error) {
	contexts, err := kubeContexts()
	if err != nil || len(contexts) == 0 {
		return "", "", fmt.Errorf("no kubeconfig contexts found — is kubectl configured?")
	}
	cursor := 0
	for i, c := range contexts {
		if c == current {
			cursor = i
		}
	}
	ti := textinput.New()
	ti.Placeholder = defaultNS
	ti.CharLimit = 63
	m := pickerModel{contexts: contexts, cursor: cursor, current: current, defaultNS: defaultNS, input: ti}
	// One context = no choice to make; go straight to the namespace step.
	if len(contexts) == 1 {
		m.chosenCtx = contexts[0]
		m.step = stepLoadingNamespaces
	}
	out, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", "", err
	}
	final, ok := out.(pickerModel)
	if !ok {
		return "", "", fmt.Errorf("picker returned unexpected model %T", out)
	}
	if final.aborted {
		return "", "", fmt.Errorf("no target chosen — rerun and pick, or pass --context <name>")
	}
	return final.chosenCtx, final.chosenNS, nil
}

const createNewSentinel = "+ create a new namespace…"

type nsListMsg struct {
	names []string
	err   error
}

// pickerStep is where the two-stage picker currently is.
type pickerStep int

const (
	stepContexts pickerStep = iota
	stepLoadingNamespaces
	stepNamespaces
	stepTypeNewNamespace
)

type pickerModel struct {
	step      pickerStep
	contexts  []string
	current   string
	cursor    int
	nsItems   []string
	nsCursor  int
	nsErr     string
	defaultNS string
	input     textinput.Model
	chosenCtx string
	chosenNS  string
	aborted   bool
}

func (m pickerModel) Init() tea.Cmd {
	if m.step == stepLoadingNamespaces {
		return listNamespaces(m.chosenCtx)
	}
	return nil
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case nsListMsg:
		if msg.err != nil || len(msg.names) == 0 {
			// The listing doubles as the auth probe. Show the cluster's own
			// words (an expired SSO names its cure) and offer the way back.
			m.nsErr = ""
			if msg.err != nil {
				e := msg.err.Error()
				if len(e) > 240 {
					e = e[:240] + "…"
				}
				m.nsErr = "cannot reach " + m.chosenCtx + ": " + e
			}
			m.nsItems = nil
			m.step = stepTypeNewNamespace
			m.input.Focus()
			return m, textinput.Blink
		}
		m.nsItems = append(msg.names, createNewSentinel)
		m.nsCursor = 0
		for i, n := range msg.names {
			if n == m.defaultNS {
				m.nsCursor = i
			}
		}
		m.step = stepNamespaces
		return m, nil

	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" || (key == "q" && m.step != 3) || (key == "esc" && m.step != 3) {
			m.aborted = true
			return m, tea.Quit
		}
		switch m.step {
		case 0:
			return m.updateContexts(key)
		case 2:
			return m.updateNamespaces(key)
		case 3:
			return m.updateNewNS(key, msg)
		}
	}
	return m, nil
}

const keyEnter = "enter"

func (m pickerModel) updateContexts(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.contexts)-1 {
			m.cursor++
		}
	case keyEnter:
		m.chosenCtx = m.contexts[m.cursor]
		m.step = stepLoadingNamespaces
		return m, listNamespaces(m.chosenCtx)
	}
	return m, nil
}

func (m pickerModel) updateNamespaces(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.nsCursor > 0 {
			m.nsCursor--
		}
	case "down", "j":
		if m.nsCursor < len(m.nsItems)-1 {
			m.nsCursor++
		}
	case keyEnter:
		if m.nsItems[m.nsCursor] == createNewSentinel {
			m.step = stepTypeNewNamespace
			m.input.Focus()
			return m, textinput.Blink
		}
		m.chosenNS = m.nsItems[m.nsCursor]
		return m, tea.Quit
	}
	return m, nil
}

func (m pickerModel) updateNewNS(key string, msg tea.Msg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		if len(m.nsItems) > 0 {
			m.step = stepNamespaces
			return m, nil
		}
		// No namespace list means we came from a failed probe — back to
		// the context list, not out the door.
		m.step = stepContexts
		m.nsErr = ""
		return m, nil
	case keyEnter:
		ns := strings.TrimSpace(m.input.Value())
		if ns == "" {
			ns = m.defaultNS
		}
		m.chosenNS = ns
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string {
	var b strings.Builder
	b.WriteString(styleBrand.Render("◇ tiny") + styleSubtle.Render("  pick cluster and namespace · enter-enter repeats last · --context/-n skip") + "\n\n")
	switch m.step {
	case 0:
		for i, c := range m.contexts {
			mark := "  "
			if i == m.cursor {
				mark = styleKey.Render("▸ ")
			}
			label := c
			if c == m.current {
				label += styleSubtle.Render("  (current)")
			}
			b.WriteString(mark + label + "\n")
		}
		b.WriteString("\n" + styleSubtle.Render("  ↑/↓ move · enter choose · q quit") + "\n")
	case 1:
		b.WriteString("  " + m.chosenCtx + "\n\n" + styleSubtle.Render("  loading namespaces…") + "\n")
	case 2:
		b.WriteString("  " + m.chosenCtx + " — namespace:\n\n")
		for i, n := range m.nsItems {
			mark := "  "
			if i == m.nsCursor {
				mark = styleKey.Render("▸ ")
			}
			b.WriteString(mark + n + "\n")
		}
		b.WriteString("\n" + styleSubtle.Render("  ↑/↓ move · enter choose · q quit") + "\n")
	case 3:
		if m.nsErr != "" {
			b.WriteString("  " + styleWarn.Render("✳ ") + m.nsErr + "\n\n")
			b.WriteString("  namespace to use anyway:\n\n  " + m.input.View() + "\n\n" + styleSubtle.Render("  enter confirm · esc back to contexts") + "\n")
			break
		}
		b.WriteString("  new namespace name:\n\n  " + m.input.View() + "\n\n" + styleSubtle.Render("  enter confirm · esc back") + "\n")
	}
	return b.String()
}

// listNamespaces asks the CHOSEN cluster what namespaces it has.
func listNamespaces(ctxName string) tea.Cmd {
	return func() tea.Msg {
		k, err := kube.NewClient(kube.Options{Context: ctxName, Namespace: "default"})
		if err != nil {
			return nsListMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		list := &corev1.NamespaceList{}
		if err := k.Client.List(ctx, list); err != nil {
			return nsListMsg{err: err}
		}
		names := make([]string, 0, len(list.Items))
		for _, n := range list.Items {
			names = append(names, n.Name)
		}
		return nsListMsg{names: names}
	}
}
