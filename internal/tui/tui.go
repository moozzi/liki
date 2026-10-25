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

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"liki/internal/cli"
	"liki/internal/store"
)

// builtins are commands handled by the interactive mode itself.
var builtins = []cli.Command{
	{Name: "help", Summary: "show commands and shortcuts"},
	{Name: "clear", Summary: "clear the screen"},
	{Name: "exit", Summary: "quit liki"},
}

var (
	accent    = lipgloss.Color("6")
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	echoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	boxStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	selStyle  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	nameStyle = lipgloss.NewStyle().Foreground(accent)
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

	// running is the command currently executing, "" when idle.
	running string
	cancel  context.CancelFunc
	// queue holds lines entered while a command was running.
	queue []string

	suggestions []cli.Command
	selected    int

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
	return tea.Sequence(tea.ClearScreen, tea.Println(banner()), textinput.Blink)
}

func banner() string {
	title := lipgloss.NewStyle().Foreground(accent).Bold(true).Render("liki")
	info := "local wiki for links"
	if s, err := openStore(); err == nil {
		if links, err := s.List(); err == nil {
			info = fmt.Sprintf("%d links saved", len(links))
		}
		s.Close()
	}
	help := dim.Render("type to search · / for commands · ↑↓ history · ctrl+c to exit")
	return boxStyle.Render(title+" "+dim.Render(info)+"\n"+help) + "\n"
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
		return m, nil

	case outputMsg:
		return m, tea.Sequence(tea.Println(msg.line), msg.next)

	case doneMsg:
		m.running, m.cancel = "", nil
		var report tea.Cmd
		switch {
		case errors.Is(msg.err, context.Canceled):
			// Cancelling also drops what was queued behind the command.
			m.queue = nil
			report = tea.Println(dim.Render("cancelled"))
		case msg.err != nil:
			report = tea.Println(errStyle.Render("error: " + msg.err.Error()))
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
	echo := tea.Println(echoStyle.Render("> " + line))

	if !strings.HasPrefix(line, "/") {
		return tea.Sequence(echo, m.start(line, append([]string{"search"}, strings.Fields(line)...)))
	}
	args, err := splitArgs(line[1:])
	if err != nil {
		return tea.Sequence(echo, tea.Println(errStyle.Render("error: "+err.Error())))
	}
	if len(args) == 0 {
		return echo
	}
	switch args[0] {
	case "exit", "quit", "q":
		return tea.Quit
	case "clear":
		return tea.ClearScreen
	case "help", "?":
		return tea.Sequence(echo, tea.Println(helpText()))
	}
	if !known(args[0]) {
		return tea.Sequence(echo, tea.Println(errStyle.Render(fmt.Sprintf("unknown command /%s - type /help", args[0]))))
	}
	return tea.Sequence(echo, m.start(line, args))
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
		b.WriteString("  " + nameStyle.Render("/"+c.Name))
		if c.Args != "" {
			b.WriteString(" " + dim.Render(c.Args))
		}
		b.WriteString("\n      " + c.Summary + "\n")
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
	b.WriteString(boxStyle.Width(max(20, m.width-2)).Render(m.input.View()) + "\n")
	for i, c := range m.suggestions {
		name := fmt.Sprintf("%-8s", "/"+c.Name)
		if i == m.selected {
			b.WriteString(selStyle.Render("› "+name) + " " + c.Summary + "\n")
		} else {
			b.WriteString(dim.Render("  "+name+" "+c.Summary) + "\n")
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
