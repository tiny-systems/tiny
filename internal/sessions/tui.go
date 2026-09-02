package sessions

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

/*
The fleet screen. One table, three truths per row: what runs, what it is
doing, and whether it needs you. ✳ floats to the top.

	✳ flaky-test    needs you   what color should the bikeshed be?
	● api-fix       running     fix the flaky checkout test
	✓ readme        done

enter attaches (kubectl exec → tmux attach — detach with ctrl-q d), a
answers the selected question inline, n starts a session, d deletes one,
q quits. The screen refreshes itself; there is nothing to remember.
*/

var (
	glyphAmber = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	glyphGreen = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	glyphFaint = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	rowSel     = lipgloss.NewStyle().Bold(true)
	helpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	// Brand: tiny is spring green (84 on deep green 22) — claude is coral,
	// codex is teal; a fleet glance should know whose screen it is.
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("84")).Background(lipgloss.Color("22")).Padding(0, 1)
)

type mode int

const (
	modeList mode = iota
	modeAnswer
	modeBroadcast
	modeForm
	modeSettings
	modeMessage
	modeRunnerEdit
)

// formLabels order the options form; CreateOpts is filled in this order.
// formHints show as placeholders: what an EMPTY field means.
var (
	formLabels = []string{"task", "name", "repo", "image", "agent", "model", "cpu", "memory"}
	formHints  = []string{
		"optional — blank session you attach and brief",
		"generated (s-xxxxx)",
		"none — empty workspace",
		"default agent image (any glibc image with git works)",
		"claude — or codex",
		"agent default — e.g. claude-sonnet-5, gpt-5.2-codex",
		"cluster default, e.g. 500m or 2",
		"cluster default, e.g. 2Gi (becomes the limit too)",
	}
)

type snapshotMsg struct {
	snap *Snapshot
	err  error
}

type tickMsg struct{}

type actionDoneMsg struct{ err error }

// broadcastDoneMsg reports how far the megaphone reached.
type broadcastDoneMsg struct {
	n   int
	err error
}

// Model is the TUI state.
type Model struct {
	store *Store
	// attach is how a row becomes a terminal; injected so tests don't exec.
	attach func(row Row) tea.Cmd

	snap   *Snapshot
	cursor int
	mode   mode
	input  textinput.Model
	// target of the answer being typed
	answering *Row
	status    string
	err       error
	width     int
	height    int
	form      []textinput.Model
	formIdx   int
	// pendingDelete holds the session d was pressed on; y confirms,
	// anything else forgets it.
	pendingDelete string
	// messaging is the row m was pressed on.
	messaging *Row
	settings  NamespaceSettings
	setIdx    int
}

// NewModel builds the fleet screen over a store.
func NewModel(store *Store) Model {
	ti := textinput.New()
	ti.CharLimit = 500
	return Model{store: store, input: ti, attach: storeAttach(store)}
}

// Init starts the refresh loop.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.load(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) load() tea.Cmd {
	store := m.store
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snap, err := store.Load(ctx)
		return snapshotMsg{snap, err}
	}
}

// Update is the event loop.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case uploadProgressMsg:
		m.status = fmt.Sprintf("%s  %d%%", msg.label, msg.pct)
		return m, watchProgress(msg.ch, msg.label)

	case uploadDoneMsg:
		m.status = "✓ " + msg.remote + " in " + msg.session + " (agent notified)"
		return m, m.load()

	case settingsMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
		}
		m.settings = msg.ns
		m.status = ""
		return m, nil

	case snapshotMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.snap = msg.snap
		// The list ends two rows past the sessions: the "new" items are
		// real cursor targets and a refresh must not evict the cursor
		// from them.
		if maxIdx := len(m.snap.Rows) + 4; m.cursor > maxIdx {
			m.cursor = maxIdx
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// A resize mid-frame (a terminal banner appearing) can leave ghost
		// lines behind the diff; a full clear makes the next paint whole.
		return m, tea.ClearScreen

	case tickMsg:
		return m, tea.Batch(m.load(), tick())

	case actionDoneMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.status = ""
		}
		return m, m.load()

	case broadcastDoneMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		m.status = fmt.Sprintf("✉ delivered to %d session(s)", msg.n)
		return m, m.load()

	case tea.KeyMsg:
		if m.mode == modeSettings {
			return m.updateSettings(msg)
		}
		if m.mode == modeRunnerEdit {
			return m.updateRunnerEdit(msg)
		}
		if m.mode == modeForm {
			return m.updateForm(msg)
		}
		if m.mode != modeList {
			return m.updateInput(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingDelete != "" && msg.String() != "y" {
		m.pendingDelete = ""
		m.status = ""
	}
	// A file dragged onto the terminal arrives as a pasted path: upload it
	// into the selected session's workspace and tell the agent.
	if msg.Paste {
		return m.handleDrop(string(msg.Runes))
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case keyUp, "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case keyDown, "j":
		if m.snap != nil && m.cursor < len(m.snap.Rows)+4 {
			m.cursor++
		}
	case keyEnter:
		if m.snap != nil && m.cursor >= len(m.snap.Rows) {
			return m.enterVirtualRow(m.cursor - len(m.snap.Rows))
		}
		if row, ok := m.selected(); ok {
			if row.Pod == "" {
				m.err = fmt.Errorf("session %s has no pod yet", row.Name)
				return m, nil
			}
			// Attaching is the acknowledgement — idle "waiting for you"
			// cards clear the moment the human looks.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			m.store.ClearAttention(ctx, row.Name)
			cancel()
			return m, m.attach(row)
		}
	case "a":
		if row, ok := m.selected(); ok && row.NeedsHuman() {
			r := row
			m.answering = &r
			m.mode = modeAnswer
			m.input.Reset()
			if opts := row.Question.Spec.Options; len(opts) > 0 {
				m.input.Placeholder = strings.Join(opts, " / ")
			} else {
				m.input.Placeholder = "your answer"
			}
			return m, m.input.Focus()
		}
	case "n":
		return m.openQuickNew()
	case "o":
		return m.openForm(), textinput.Blink
	case "m":
		if row, ok := m.selected(); ok {
			r := row
			m.messaging = &r
			m.mode = modeMessage
			m.input.Placeholder = "message to " + row.Name
			m.input.SetValue("")
			return m, m.input.Focus()
		}
	case "b":
		if m.snap != nil && len(m.snap.Rows) > 0 {
			return m.openBroadcast()
		}
	case "d", "y":
		return m.updateDelete(msg.String())
	case "r":
		return m, m.load()
	}
	return m, nil
}

// updateDelete is the two-keystroke delete: d arms, y fires.
func (m Model) updateDelete(key string) (tea.Model, tea.Cmd) {
	if key == "d" {
		if row, ok := m.selected(); ok {
			// A session is a workspace and a transcript — never gone on one
			// keystroke.
			m.pendingDelete = row.Name
			m.status = "delete " + row.Name + "? its workspace and transcript go too  [y/N]"
		}
		return m, nil
	}
	if m.pendingDelete == "" {
		return m, nil
	}
	name, store := m.pendingDelete, m.store
	m.pendingDelete = ""
	m.status = "deleting " + name
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return actionDoneMsg{store.Delete(ctx, name)}
	}
}

func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyEsc:
		m.mode = modeList
		m.answering = nil
		return m, nil
	case keyEnter:
		text := strings.TrimSpace(m.input.Value())
		store := m.store
		switch m.mode {
		case modeMessage:
			row := m.messaging
			m.mode = modeList
			m.messaging = nil
			m.status = "sending…"
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				return actionDoneMsg{store.SendText(ctx, row.Name, text)}
			}
		case modeBroadcast:
			m.mode = modeList
			m.status = "broadcasting…"
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				delivered, err := store.Broadcast(ctx, text)
				return broadcastDoneMsg{n: len(delivered), err: err}
			}
		case modeAnswer:
			q := m.answering.Question.Name
			m.mode = modeList
			m.answering = nil
			m.status = "answering…"
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				return actionDoneMsg{store.Answer(ctx, q, text)}
			}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// openQuickNew creates a blank session on the spot — no questions. Attach
// and brief it; it names itself. The options form is where parameters live.
func (m Model) openQuickNew() (Model, tea.Cmd) {
	store := m.store
	m.status = "starting a blank session…"
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := store.Create(ctx, CreateOpts{})
		return actionDoneMsg{err}
	}
}

func (m Model) openForm() Model {
	m.mode = modeForm
	m.form = make([]textinput.Model, len(formLabels))
	for i := range formLabels {
		ti := textinput.New()
		ti.Placeholder = formHints[i]
		ti.CharLimit = 500
		ti.Width = 56
		m.form[i] = ti
	}
	m.formIdx = 0
	m.form[0].Focus()
	return m
}

const (
	keyEnter = "enter"
	keyUp    = "up"
	keyDown  = "down"
	keyEsc   = "esc"
)

// settingsItems orders the switchboard screen.
var settingsItems = []string{
	"zot registry cache — one Hub pull per image per namespace",
	"node trust for the cache — DaemonSet installs its CA on every node (cluster-touching)",
	"minio artifact store — sessions hand each other files (mc alias: store)",
	"GitHub runner — issues labeled `tiny` become sessions (org or owner/repo; enter edits, empty = off)",
}

func (m Model) openSettings() (tea.Model, tea.Cmd) {
	store := m.store
	m.mode = modeSettings
	m.setIdx = 0
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ns, err := store.LoadSettings(ctx)
		return settingsMsg{ns, err}
	}
}

type uploadDoneMsg struct {
	session string
	remote  string
}

type uploadProgressMsg struct {
	label string
	pct   int
	ch    chan int
}

// watchProgress re-arms itself per tick; the channel closing ends it.
func watchProgress(ch chan int, label string) tea.Cmd {
	return func() tea.Msg {
		pct, ok := <-ch
		if !ok {
			return nil
		}
		return uploadProgressMsg{label: label, pct: pct, ch: ch}
	}
}

type settingsMsg struct {
	ns  NamespaceSettings
	err error
}

func (m Model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyEsc, "q":
		m.mode = modeList
		return m, nil
	case keyUp, "k":
		if m.setIdx > 0 {
			m.setIdx--
		}
	case keyDown, "j":
		if m.setIdx < len(settingsItems)-1 {
			m.setIdx++
		}
	case " ", keyEnter:
		switch m.setIdx {
		case 0:
			m.settings.Zot = !m.settings.Zot
			if !m.settings.Zot {
				m.settings.ZotNodeTrust = false // trust without a cache is noise
			}
		case 1:
			m.settings.ZotNodeTrust = !m.settings.ZotNodeTrust
		case 2:
			m.settings.Minio = !m.settings.Minio
		case 3:
			// Text, not a toggle: edit the watched org/repo.
			m.mode = modeRunnerEdit
			m.input.Placeholder = "org or owner/repo — empty turns the runner off"
			m.input.SetValue(m.settings.RunnerRepo)
			return m, m.input.Focus()
		}
		store, snap := m.store, m.settings
		m.status = "saving…"
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := store.SaveSettings(ctx, snap); err != nil {
				return settingsMsg{snap, err}
			}
			ns, err := store.LoadSettings(ctx)
			return settingsMsg{ns, err}
		}
	}
	return m, nil
}

func (m Model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyEsc:
		m.mode = modeList
		return m, nil
	case "tab", keyDown:
		m.form[m.formIdx].Blur()
		m.formIdx = (m.formIdx + 1) % len(m.form)
		m.form[m.formIdx].Focus()
		return m, textinput.Blink
	case "shift+tab", keyUp:
		m.form[m.formIdx].Blur()
		m.formIdx = (m.formIdx + len(m.form) - 1) % len(m.form)
		m.form[m.formIdx].Focus()
		return m, textinput.Blink
	case keyEnter:
		if m.formIdx < len(m.form)-1 {
			m.form[m.formIdx].Blur()
			m.formIdx++
			m.form[m.formIdx].Focus()
			return m, textinput.Blink
		}
		opts := CreateOpts{
			Task:   strings.TrimSpace(m.form[0].Value()),
			Name:   strings.TrimSpace(m.form[1].Value()),
			Repo:   strings.TrimSpace(m.form[2].Value()),
			Image:  strings.TrimSpace(m.form[3].Value()),
			Agent:  strings.TrimSpace(m.form[4].Value()),
			Model:  strings.TrimSpace(m.form[5].Value()),
			CPU:    strings.TrimSpace(m.form[6].Value()),
			Memory: strings.TrimSpace(m.form[7].Value()),
		}
		m.mode = modeList
		m.status = "creating…"
		store := m.store
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := store.Create(ctx, opts)
			return actionDoneMsg{err}
		}
	}
	var cmd tea.Cmd
	m.form[m.formIdx], cmd = m.form[m.formIdx].Update(msg)
	return m, cmd
}

func (m Model) selected() (Row, bool) {
	if m.snap == nil || m.cursor >= len(m.snap.Rows) {
		return Row{}, false
	}
	return m.snap.Rows[m.cursor], true
}

// View renders the screen.
func (m Model) View() string {
	var b strings.Builder
	ver := m.store.Version
	if ver == "" {
		ver = "dev"
	}
	b.WriteString(titleStyle.Render("◇ tiny") + helpStyle.Render("  sessions · "+m.store.Target()+" · "+ver) + "\n\n")

	if m.snap == nil {
		b.WriteString(helpStyle.Render("  loading…") + "\n")
	}

	detailW := 60
	if m.width > 60 {
		detailW = m.width - 60
	}
	if m.snap != nil && len(m.snap.Rows) > 0 {
		b.WriteString(helpStyle.Render(fmt.Sprintf("    %-20s %-10s %5s %6s %7s  %s", "NAME", "STATE", "AGE", "CPU", "MEM", "WHAT")) + "\n")
	}
	if m.snap != nil {
		for i, row := range m.snap.Rows {
			name := trunc(row.Name, 20)
			if row.Depth > 0 {
				name = "└ " + trunc(row.Name, 18)
			}
			what := trunc(detail(row), detailW)
			// A question demands attention and the living title informs; the
			// frozen birth task is history and renders like it.
			if !row.NeedsHuman() && row.Title == "" && row.Activity == "" {
				what = helpStyle.Render(what)
			}
			line := fmt.Sprintf("%s %-20s %-10s %5s %6s %7s  %s", m.glyph(row), name, phaseWord(row), fmtAge(row.Age), orDash(row.CPU), orDash(row.Mem), what)
			if i == m.cursor {
				line = rowSel.Render("▸ " + line)
			} else {
				line = "  " + line
			}
			b.WriteString(m.clip(line) + "\n")
		}
		newIdx := len(m.snap.Rows)
		for i, label := range []string{"✉ broadcast to all…", "＋ new session", "⚙ new session with options…", "☰ namespace settings", "✕ quit"} {
			line := "  " + glyphGreen.Render("·") + " " + helpStyle.Render(label)
			if m.cursor == newIdx+i {
				line = rowSel.Render("▸ · " + label)
			}
			b.WriteString(m.clip(line) + "\n")
		}
		for _, q := range m.snap.Loose {
			b.WriteString("  " + glyphAmber.Render("✳") + " " + helpStyle.Render("(unattributed) ") + trunc(q.Spec.Text, detailW) + "\n")
		}
	}

	// The footer lives at the BOTTOM of the terminal, not under the list.
	var f strings.Builder
	switch m.mode {
	case modeSettings:
		m.renderSettings(&f)
	case modeRunnerEdit:
		f.WriteString("  GitHub runner — watch which org or repo?\n  " + m.input.View() + "\n")
	case modeForm:
		f.WriteString("  new session — enter advances, enter on last creates, esc cancels\n")
		for i, ti := range m.form {
			cur := "  "
			if i == m.formIdx {
				cur = rowSel.Render("▸ ")
			}
			fmt.Fprintf(&f, "%s%-14s %s\n", cur, formLabels[i], ti.View())
		}
	case modeMessage:
		f.WriteString("  message → " + m.messaging.Name + "  (lands in the agent's prompt)\n  " + m.input.View() + "\n")
	case modeBroadcast:
		f.WriteString("  broadcast → every unfinished session\n  " + m.input.View() + "\n")
	case modeAnswer:
		f.WriteString("  answer " + glyphAmber.Render(trunc(m.answering.Question.Spec.Text, detailW)) + "\n  " + m.input.View() + "\n")
	default:
		f.WriteString(helpStyle.Render("  "+m.listHints()) + "\n")
	}
	if m.status != "" {
		f.WriteString(helpStyle.Render("  "+m.status) + "\n")
	}
	if m.err != nil {
		f.WriteString(errStyle.Render("  "+m.err.Error()) + "\n")
	}
	body, footer := b.String(), f.String()
	if m.height > 0 {
		used := strings.Count(body, "\n") + strings.Count(footer, "\n")
		for pad := m.height - used - 1; pad > 0; pad-- {
			body += "\n"
		}
	} else {
		body += "\n"
	}
	return body + footer
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// fmtAge renders a duration the way a fleet reads it: 45s, 21m, 10h, 3d.
// clip hard-truncates a rendered line to the terminal width — a wrapped
// line desynchronizes bubbletea's diff and leaves ghost copies of rows
// behind (seen live when a terminal banner shrank the window).
func (m Model) clip(line string) string {
	if m.width <= 0 {
		return line
	}
	return ansi.Truncate(line, m.width, "")
}

func fmtAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.0fm", d.Minutes())
	case d < 48*time.Hour:
		return fmt.Sprintf("%.0fh", d.Hours())
	default:
		return fmt.Sprintf("%.0fd", d.Hours()/24)
	}
}

// enterVirtualRow dispatches the list's action rows.
func (m Model) enterVirtualRow(idx int) (tea.Model, tea.Cmd) {
	switch idx {
	case 0:
		return m.openBroadcast()
	case 1:
		return m.openQuickNew()
	case 2:
		return m.openForm(), textinput.Blink
	case 3:
		return m.openSettings()
	default:
		return m, tea.Quit
	}
}

// openBroadcast is the compose behind both the ✉ row and the b key.
func (m Model) openBroadcast() (tea.Model, tea.Cmd) {
	m.mode = modeBroadcast
	m.input.Placeholder = "broadcast to every unfinished session"
	m.input.SetValue("")
	return m, m.input.Focus()
}

func (m Model) updateRunnerEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyEsc:
		m.mode = modeSettings
		return m, nil
	case keyEnter:
		m.settings.RunnerRepo = strings.TrimSpace(m.input.Value())
		m.mode = modeSettings
		store, snap := m.store, m.settings
		m.status = "saving…"
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := store.SaveSettings(ctx, snap); err != nil {
				return settingsMsg{snap, err}
			}
			ns, err := store.LoadSettings(ctx)
			return settingsMsg{ns, err}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// renderSettings draws the switchboard with each add-on's observed truth.
func (m Model) renderSettings(f *strings.Builder) {
	f.WriteString("  namespace settings — space toggles, esc back\n")
	checks := []bool{m.settings.Zot, m.settings.ZotNodeTrust, m.settings.Minio, m.settings.RunnerRepo != ""}
	states := []string{m.settings.ZotState, "", m.settings.MinioState, m.runnerStateLabel()}
	for i, item := range settingsItems {
		cur, box := "  ", "[ ]"
		if checks[i] {
			box = "[x]"
		}
		if i == m.setIdx {
			cur = rowSel.Render("▸ ")
		}
		state := ""
		switch {
		case states[i] == "":
		case states[i] == stateRunning:
			state = "  " + glyphGreen.Render("● running")
		case states[i] == stateStarting:
			state = "  " + helpStyle.Render("◌ starting")
		case strings.HasPrefix(states[i], "watching "):
			state = "  " + helpStyle.Render(states[i])
		default:
			state = "  " + errStyle.Render("✗ "+trunc(states[i], 60))
		}
		fmt.Fprintf(f, "%s%s %s%s\n", cur, box, item, state)
	}
	key := "absent — run `tiny setup`"
	if m.settings.RepoKey {
		key = "present (tiny-repo-keys)"
	}
	f.WriteString(helpStyle.Render("      repo deploy key: "+key) + "\n")
}

// handleDrop treats a pasted existing-file path as a drop onto the
// selected session: stream-upload with live percent in the status line.
func (m Model) handleDrop(paste string) (tea.Model, tea.Cmd) {
	if path, ok := droppedPath(paste); ok {
		row, selected := m.selected()
		if !selected {
			m.status = "select a session row, then drop the file"
			return m, nil
		}
		store, name := m.store, row.Name
		base := filepath.Base(path)
		m.status = "uploading " + base + " → " + name + "…"
		prog := make(chan int, 8)
		start := func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			defer close(prog)
			lastPct := -5
			remote, err := store.UploadFile(ctx, name, path, func(done, total int64) {
				if total <= 0 {
					return
				}
				if pct := int(done * 100 / total); pct >= lastPct+5 {
					lastPct = pct
					select {
					case prog <- pct:
					default:
					}
				}
			})
			if err != nil {
				return actionDoneMsg{err}
			}
			return uploadDoneMsg{name, remote}
		}
		return m, tea.Batch(start, watchProgress(prog, "uploading "+base+" → "+name))
	}
	return m, nil
}

// listHints names only the keys that do something for the current cursor.
func (m Model) runnerStateLabel() string {
	if m.settings.RunnerRepo == "" {
		return ""
	}
	return "watching " + m.settings.RunnerRepo
}

func (m Model) listHints() string {
	if m.snap == nil {
		return "[q] quit"
	}
	switch m.cursor - len(m.snap.Rows) {
	case 0:
		return "[enter] broadcast to every unfinished session  [q] quit"
	case 1:
		return "[enter] start a blank session  [q] quit"
	case 2:
		return "[enter] open the options form  [q] quit"
	case 3:
		return "[enter] open settings  [q] quit"
	case 4:
		return "[enter] quit"
	}
	hints := "[enter] attach"
	if row, ok := m.selected(); ok && row.NeedsHuman() {
		hints += "  [a] answer"
	}
	return hints + "  [m] message  [b] broadcast  [d] delete  [n] new  [o] new with options  [q] quit"
}

func (m Model) glyph(row Row) string {
	g := row.Glyph()
	switch g {
	case "✳":
		return glyphAmber.Render(g)
	case "●", "✓":
		return glyphGreen.Render(g)
	default:
		return glyphFaint.Render(g)
	}
}

func phaseWord(row Row) string {
	if row.NeedsHuman() {
		return "needs you"
	}
	if row.Phase == "" {
		return "…"
	}
	return strings.ToLower(row.Phase)
}

func detail(row Row) string {
	if row.NeedsHuman() {
		return row.Question.Spec.Text
	}
	// A paused session (usage limit) says so before its title does.
	if strings.HasPrefix(row.Activity, "⏸") {
		return row.Activity
	}
	// A stuck session says why before anything else.
	if row.Message != "" && row.Phase != "Running" {
		return row.Message
	}
	// Declared intent first (agents keep it current), the live turn-tail
	// when no title exists, the frozen birth task last.
	if row.Title != "" {
		return row.Title
	}
	if row.Activity != "" {
		return row.Activity
	}
	return row.Task
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// kubectlAttach hands the terminal to the session's tmux. kubectl carries
// the TTY plumbing; reimplementing SPDY+raw-mode here would be a lot of code
// to replace one dependency every target user already has.
// storeAttach hands the terminal to tiny's own attach — the one that turns
// a file dropped onto the window into a workspace upload.
func storeAttach(store *Store) func(Row) tea.Cmd {
	return func(row Row) tea.Cmd {
		return tea.Exec(attachExecCommand{store: store, row: row}, func(err error) tea.Msg {
			return actionDoneMsg{err}
		})
	}
}

// attachExecCommand adapts Store.Attach to bubbletea's ExecCommand: the TUI
// releases the terminal, we own it raw until detach.
type attachExecCommand struct {
	store *Store
	row   Row
}

func (a attachExecCommand) Run() error {
	return a.store.Attach(context.Background(), a.row.Name, a.row.Pod)
}
func (a attachExecCommand) SetStdin(io.Reader)  {}
func (a attachExecCommand) SetStdout(io.Writer) {}
func (a attachExecCommand) SetStderr(io.Writer) {}
