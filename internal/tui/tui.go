// Package tui is liki's interactive mode: a prompt where plain text searches
// saved links and /commands run the CLI subcommands.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"

	"liki/internal/cli"
	"liki/internal/store"
	"liki/internal/theme"
)

// builtins are commands handled by the interactive mode itself.
var builtins = []cli.Command{
	{Name: "help", Summary: "show commands and shortcuts"},
	{Name: "clear", Summary: "clear the screen"},
	{Name: "exit", Summary: "quit liki"},
}

var (
	accent    = theme.Blue
	dim       = theme.Dim
	errStyle  = theme.Error
	boxStyle  = theme.Box
	selStyle  = theme.Strong
	nameStyle = theme.Accent
)

// Run starts the interactive mode and blocks until the user quits.
func Run() error {
	m := newModel()
	_, err := tea.NewProgram(m).Run()
	m.saveHistory()
	return err
}

type model struct {
	input   textinput.Model
	spinner spinner.Model
	width   int
	started bool // whether the banner was printed

	// running is the command currently executing, "" when idle.
	running string
	cancel  context.CancelFunc
	// queue holds lines entered while a command was running.
	queue []string
	// outStarted is set once the current command printed its first line.
	outStarted bool

	suggestions []cli.Command
	selected    int

	// printed is everything printed above the prompt, kept to print it
	// again for a new width when the terminal is resized.
	printed []printed
	// laidOut is the width printed was last laid out for.
	laidOut int
	// relayoutSoon is set after a relayout, until relayoutMsg allows the next.
	relayoutSoon bool
	// stats describes the saved links in the banner.
	stats string

	history     []string
	historyPos  int // len(history) means the line being typed
	draft       string
	historyFile string
}

func newModel() *model {
	in := textinput.New()
	in.Prompt = "> "
	in.PromptStyle = lipgloss.NewStyle().Foreground(accent)
	in.Placeholder = "search your links, or type / for commands"
	in.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(accent)

	m := &model{input: in, spinner: sp, width: 80}
	if path, err := store.DefaultPath(); err == nil {
		m.historyFile = filepath.Join(filepath.Dir(path), "history")
		m.loadHistory()
	}
	return m
}

func (m *model) Init() tea.Cmd {
	// Start on a clean screen, like a separate app.
	// The banner is printed on the first WindowSizeMsg, once the width is known.
	return textinput.Blink
}

// logo spells "liki" in half-block letters, two rows high.
var logo = [2]string{
	"█  ▀ █▄▀ ▀",
	"█▄ █ █ █ █",
}

// banner renders the logo with a tagline and stats about the saved links
// next to it; on screens too narrow for that, only the text.
func banner(width int, stats string) string {
	tagline := theme.Strong.Render("your local link wiki")
	text := lipgloss.JoinVertical(lipgloss.Left, tagline, dim.Render(stats))
	// Indented to line up with the text in the input box, with room above.
	pad := lipgloss.NewStyle().Padding(1, 0, 0, 2)
	if width < 2+lipgloss.Width(logo[0])+3+lipgloss.Width(text) {
		return pad.Render(text)
	}
	return pad.Render(lipgloss.JoinHorizontal(lipgloss.Top, gradient(logo[0])+"\n"+gradient(logo[1]), "   ", text))
}

// gradient colors s from pale to deep blue, left to right.
func gradient(s string) string {
	from, _ := colorful.Hex(string(theme.PaleBlue))
	to, _ := colorful.Hex(string(theme.Blue))
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		t := float64(i) / float64(max(1, len(r)-1))
		col := lipgloss.Color(from.BlendLab(to, t).Hex())
		b.WriteString(lipgloss.NewStyle().Foreground(col).Render(string(c)))
	}
	return b.String()
}

// loadStats describes the saved links for the banner.
func loadStats() string {
	s, err := openStore()
	if err != nil {
		return "local wiki for your links"
	}
	defer s.Close()
	st, err := s.Stats()
	if err != nil {
		return "local wiki for your links"
	}
	return statsLine(st)
}

func statsLine(st store.Stats) string {
	parts := []string{plural(st.Links, "link")}
	if st.Tags > 0 {
		parts = append(parts, plural(st.Tags, "tag"))
	}
	if !st.LastAdded.IsZero() {
		parts = append(parts, "last added "+ago(time.Since(st.LastAdded)))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// ago describes a duration in the past, roughly.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	}
}

func openStore() (*store.Store, error) {
	path, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	return store.Open(path)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.input.Width = max(10, msg.Width-8)
		if !m.started {
			// Start on a clean screen, like a separate app.
			m.started, m.laidOut = true, m.width
			return m, tea.Sequence(tea.ClearScreen, m.printBanner())
		}
		return m, m.relayout()

	case relayoutMsg:
		m.relayoutSoon = false
		return m, m.relayout()

	case outputMsg:
		return m, tea.Sequence(m.println(m.outLine(msg.line)), msg.next)

	case doneMsg:
		m.running, m.cancel = "", nil
		var report tea.Cmd
		switch {
		case errors.Is(msg.err, context.Canceled):
			// Cancelling also drops what was queued behind the command.
			m.queue = nil
			report = m.block(dim.Render("cancelled"))
		case msg.err != nil:
			report = m.block(errStyle.Render("error: " + msg.err.Error()))
		}
		if len(m.queue) > 0 {
			next := m.queue[0]
			m.queue = m.queue[1:]
			return m, tea.Sequence(report, m.execute(next))
		}
		return m, report

	case spinner.TickMsg:
		if m.running == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateSuggestions()
	return m, cmd
}

// handleKey processes keys with a special meaning; others go to the input.
func (m *model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.Type {
	case tea.KeyCtrlC:
		switch {
		case m.running != "":
			m.cancel()
		case m.input.Value() != "":
			m.input.Reset()
			m.updateSuggestions()
		default:
			return tea.Quit, true
		}
		return nil, true

	case tea.KeyCtrlD:
		if m.input.Value() == "" && m.running == "" {
			return tea.Quit, true
		}

	case tea.KeyEsc:
		m.input.Reset()
		m.updateSuggestions()
		return nil, true

	case tea.KeyUp, tea.KeyDown:
		delta := 1
		if msg.Type == tea.KeyUp {
			delta = -1
		}
		if len(m.suggestions) > 0 {
			m.selected = (m.selected + delta + len(m.suggestions)) % len(m.suggestions)
		} else {
			m.browseHistory(delta)
		}
		return nil, true

	case tea.KeyTab:
		if len(m.suggestions) > 0 {
			m.complete()
		}
		return nil, true

	case tea.KeyEnter:
		if len(m.suggestions) > 0 {
			c := m.suggestions[m.selected]
			// Commands that need arguments are completed first, the rest run right away.
			if strings.HasPrefix(c.Args, "<") && !strings.HasPrefix(m.input.Value(), "/"+c.Name+" ") {
				m.complete()
				return nil, true
			}
			m.input.SetValue("/" + c.Name)
		}
		line := strings.TrimSpace(m.input.Value())
		m.input.Reset()
		m.updateSuggestions()
		if line == "" {
			return nil, true
		}
		m.addHistory(line)
		if m.running != "" {
			m.queue = append(m.queue, line)
			return nil, true
		}
		return m.execute(line), true
	}
	return nil, false
}

// execute runs one input line: a /command, or a search for plain text.
func (m *model) execute(line string) tea.Cmd {
	switch line {
	// Plain "exit" and "quit" also quit, like in a shell; to search for these
	// words use /search.
	case "/exit", "/quit", "/q", "exit", "quit":
		return tea.Quit
	case "/clear":
		m.printed = nil
		return tea.Sequence(tea.ClearScreen, m.printBanner())
	}

	// Like a message in Claude Code: a blank line, then the input itself.
	m.outStarted = false
	echo := tea.Sequence(m.println(""), m.println(theme.Accent.Render(">")+" "+line))

	if !strings.HasPrefix(line, "/") {
		return tea.Sequence(echo, m.start(line, append([]string{"search"}, strings.Fields(line)...)))
	}
	args, err := splitArgs(line[1:])
	if err != nil {
		return tea.Sequence(echo, m.block(errStyle.Render("error: "+err.Error())))
	}
	if len(args) == 0 {
		return echo
	}
	switch args[0] {
	case "help", "?":
		return tea.Sequence(echo, m.block(helpText()))
	}
	if !known(args[0]) {
		return tea.Sequence(echo, m.block(errStyle.Render(fmt.Sprintf("unknown command /%s - type /help", args[0]))))
	}
	return tea.Sequence(echo, m.start(line, args))
}

// Command output is shown as a block under the input, the first line marked
// with "⎿", like tool results in Claude Code.
const outIndent = 5

// outLine prefixes one line of command output for the block.
func (m *model) outLine(s string) string {
	if s == "" {
		return ""
	}
	if !m.outStarted {
		m.outStarted = true
		return dim.Render("  ⎿  ") + s
	}
	return strings.Repeat(" ", outIndent) + s
}

// block prints text as command output.
func (m *model) block(text string) tea.Cmd {
	var cmds []tea.Cmd
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		cmds = append(cmds, m.println(m.outLine(l)))
	}
	return tea.Sequence(cmds...)
}

// printed is a piece of output above the prompt.
type printed struct {
	text   string
	banner bool // the banner, drawn for the current width instead of text
}

// maxPrinted limits how much output is kept for printing again after a resize.
const maxPrinted = 2000

// relayoutEvery limits how often output is laid out again while the
// terminal is being resized.
const relayoutEvery = 50 * time.Millisecond

// relayoutMsg allows the next relayout.
type relayoutMsg struct{}

// relayout prints the output again if the width changed since it was last
// laid out: at once, and then at most every relayoutEvery, so dragging the
// window edge stays smooth and the output ends up laid out for the final width.
func (m *model) relayout() tea.Cmd {
	if m.width == m.laidOut || m.relayoutSoon {
		return nil
	}
	m.laidOut, m.relayoutSoon = m.width, true
	return tea.Batch(m.reprint(), tea.Tick(relayoutEvery, func(time.Time) tea.Msg { return relayoutMsg{} }))
}

// println prints text above the prompt and remembers it.
func (m *model) println(text string) tea.Cmd {
	return m.add(printed{text: text})
}

func (m *model) printBanner() tea.Cmd {
	m.stats = loadStats()
	return m.add(printed{banner: true})
}

func (m *model) add(p printed) tea.Cmd {
	m.printed = append(m.printed, p)
	if len(m.printed) > maxPrinted {
		m.printed = m.printed[len(m.printed)-maxPrinted:]
	}
	return tea.Println(m.render(p))
}

func (m *model) render(p printed) string {
	if p.banner {
		return banner(m.width, m.stats)
	}
	lines := strings.Split(p.text, "\n")
	for i, l := range lines {
		lines[i] = layout(l, m.width)
	}
	return strings.Join(lines, "\n")
}

// clearAll clears the screen and the scrollback and moves the cursor home.
const clearAll = "\x1b[2J\x1b[3J\x1b[H"

// reprint replaces everything on the terminal with the output laid out for
// the current width. The terminal rewraps old lines on resize, which leaves
// the output misaligned and the prompt drawn over remains of its old self.
func (m *model) reprint() tea.Cmd {
	var b strings.Builder
	b.WriteString(clearAll)
	for i, p := range m.printed {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.render(p))
	}
	return tea.Println(b.String())
}

// layout aligns the right column of a row (after cli.ColumnSep) to the right
// edge, where the text in the input box ends, shortening the left part to
// make room. Other lines are returned as they are.
func layout(line string, width int) string {
	left, right, ok := strings.Cut(line, cli.ColumnSep)
	if !ok {
		return line
	}
	const gap = 3
	room := max(10, width-2-gap-ansi.StringWidth(right))
	left = ansi.Truncate(left, room, "…")
	return left + strings.Repeat(" ", room-ansi.StringWidth(left)+gap) + right
}

func known(name string) bool {
	for _, c := range cli.Commands {
		if c.Name == name {
			return true
		}
	}
	// Aliases accepted by cli.Run.
	switch name {
	case "ls", "remove", "delete", "read", "find", "s":
		return true
	}
	return false
}

func helpText() string {
	var b strings.Builder
	b.WriteString("Type any text to search your links. Commands:\n\n")
	for _, c := range slices.Concat(cli.Commands, builtins) {
		b.WriteString(nameStyle.Render("/" + c.Name))
		if c.Args != "" {
			b.WriteString(" " + dim.Render(c.Args))
		}
		b.WriteString("\n  " + c.Summary + "\n")
	}
	b.WriteString("\n" + dim.Render("tab completes · ↑↓ history · esc clears · ctrl+c cancels a running command or exits"))
	return b.String()
}

func (m *model) View() string {
	var b strings.Builder
	if m.running != "" {
		b.WriteString(m.spinner.View() + " " + dim.Render(m.running+" · ctrl+c to cancel") + "\n")
		for _, q := range m.queue {
			b.WriteString(dim.Render("  queued: "+q) + "\n")
		}
	}
	b.WriteString("\n" + boxStyle.Width(max(20, m.width-2)).Render(m.input.View()) + "\n")
	if len(m.suggestions) == 0 {
		b.WriteString(dim.Render("  / for commands · ↑↓ history · esc to clear · ctrl+c to exit") + "\n")
	}
	for i, c := range m.suggestions {
		name := fmt.Sprintf("  %-9s", "/"+c.Name)
		if i == m.selected {
			b.WriteString(selStyle.Render(name) + " " + c.Summary + "\n")
		} else {
			b.WriteString(dim.Render(name+" "+c.Summary) + "\n")
		}
	}
	return b.String()
}

// updateSuggestions lists commands matching the typed "/prefix".
func (m *model) updateSuggestions() {
	v := m.input.Value()
	m.suggestions = nil
	if strings.HasPrefix(v, "/") && !strings.Contains(v, " ") {
		prefix := v[1:]
		for _, c := range slices.Concat(cli.Commands, builtins) {
			if strings.HasPrefix(c.Name, prefix) {
				m.suggestions = append(m.suggestions, c)
			}
		}
	}
	if m.selected >= len(m.suggestions) {
		m.selected = 0
	}
}

func (m *model) complete() {
	m.input.SetValue("/" + m.suggestions[m.selected].Name + " ")
	m.input.CursorEnd()
	m.updateSuggestions()
}

func (m *model) browseHistory(delta int) {
	pos := m.historyPos + delta
	if pos < 0 || pos > len(m.history) {
		return
	}
	if m.historyPos == len(m.history) {
		m.draft = m.input.Value()
	}
	m.historyPos = pos
	if pos == len(m.history) {
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.history[pos])
	}
	m.input.CursorEnd()
}

const maxHistory = 500

func (m *model) addHistory(line string) {
	if n := len(m.history); n == 0 || m.history[n-1] != line {
		m.history = append(m.history, line)
	}
	if len(m.history) > maxHistory {
		m.history = m.history[len(m.history)-maxHistory:]
	}
	m.historyPos, m.draft = len(m.history), ""
}

func (m *model) loadHistory() {
	data, err := os.ReadFile(m.historyFile)
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			m.history = append(m.history, l)
		}
	}
	m.historyPos = len(m.history)
}

func (m *model) saveHistory() {
	if m.historyFile == "" || len(m.history) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.historyFile), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(m.historyFile, []byte(strings.Join(m.history, "\n")+"\n"), 0o600)
}
