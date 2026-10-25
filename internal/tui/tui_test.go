package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"liki/internal/cli"
)

// testModel returns a model whose history lives in a temporary directory.
func testModel(t *testing.T, history ...string) *model {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LIKI_DB", filepath.Join(dir, "liki.db"))
	if len(history) > 0 {
		os.WriteFile(filepath.Join(dir, "history"), []byte(strings.Join(history, "\n")+"\n"), 0o600)
	}
	return newModel()
}

// typeText types s into the input and refreshes suggestions, like Update does.
func typeText(m *model, s string) {
	m.input.SetValue(s)
	m.updateSuggestions()
}

func press(m *model, k tea.KeyType) tea.Cmd {
	cmd, _ := m.handleKey(tea.KeyMsg{Type: k})
	return cmd
}

func TestEnterCompletesCommandsThatNeedArguments(t *testing.T) {
	m := testModel(t)
	// While a command runs, entered lines are queued, which shows what would run.
	m.running = "busy"

	typeText(m, "/ed")
	press(m, tea.KeyEnter)
	if got := m.input.Value(); got != "/edit " || len(m.queue) != 0 {
		t.Errorf("enter on /ed: input %q, queue %q; want completion to \"/edit \"", got, m.queue)
	}

	typeText(m, "/fet")
	press(m, tea.KeyEnter)
	if !slices.Equal(m.queue, []string{"/fetch"}) {
		t.Errorf("enter on /fet: queue %q; want /fetch to run, its arguments are optional", m.queue)
	}

	typeText(m, "/rm 3")
	press(m, tea.KeyEnter)
	if m.queue[len(m.queue)-1] != "/rm 3" {
		t.Errorf("enter after arguments: queue %q", m.queue)
	}
}

func TestSuggestionsCycle(t *testing.T) {
	m := testModel(t)
	typeText(m, "/")
	n := len(m.suggestions)
	if n != len(cli.Commands)+len(builtins) {
		t.Fatalf("/ suggests %d commands, want all %d", n, len(cli.Commands)+len(builtins))
	}
	press(m, tea.KeyUp)
	if m.selected != n-1 {
		t.Errorf("up from the first suggestion selects %d, want the last (%d)", m.selected, n-1)
	}
	press(m, tea.KeyDown)
	if m.selected != 0 {
		t.Errorf("down from the last suggestion selects %d, want 0", m.selected)
	}
	typeText(m, "/list ")
	if len(m.suggestions) != 0 {
		t.Errorf("suggestions shown while typing arguments: %v", m.suggestions)
	}
}

func TestHistory(t *testing.T) {
	m := testModel(t, "enum postgres", "/list")

	typeText(m, "draft")
	press(m, tea.KeyUp)
	press(m, tea.KeyUp)
	if got := m.input.Value(); got != "enum postgres" {
		t.Errorf("two ups: %q, want the oldest entry", got)
	}
	press(m, tea.KeyUp) // already at the oldest
	press(m, tea.KeyDown)
	press(m, tea.KeyDown)
	if got := m.input.Value(); got != "draft" {
		t.Errorf("back down: %q, want the unsent draft", got)
	}

	m.running = "busy"
	for _, line := range []string{"/list", "maps", "maps"} {
		typeText(m, line)
		press(m, tea.KeyEnter)
	}
	m.saveHistory()
	data, _ := os.ReadFile(m.historyFile)
	// Repeated lines are kept once.
	if got, want := string(data), "enum postgres\n/list\nmaps\n"; got != want {
		t.Errorf("saved history %q, want %q", got, want)
	}
}

func TestCtrlC(t *testing.T) {
	m := testModel(t)
	typeText(m, "some text")
	if cmd := press(m, tea.KeyCtrlC); cmd != nil || m.input.Value() != "" {
		t.Errorf("ctrl+c with text: input %q; want it cleared, not quit", m.input.Value())
	}

	cancelled := false
	m.running, m.cancel = "busy", func() { cancelled = true }
	if cmd := press(m, tea.KeyCtrlC); cmd != nil || !cancelled {
		t.Error("ctrl+c while running should cancel the command, not quit")
	}

	m.running = ""
	if cmd := press(m, tea.KeyCtrlC); cmd == nil {
		t.Fatal("ctrl+c on an empty prompt should quit")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+c on an empty prompt returned a command that does not quit")
	}
}

// TestKnownCommandsRun checks that everything the prompt accepts as a
// command is also understood by the CLI, aliases included.
func TestKnownCommandsRun(t *testing.T) {
	t.Setenv("LIKI_DB", filepath.Join(t.TempDir(), "liki.db"))
	names := []string{"ls", "remove", "delete", "read", "find", "s"}
	for _, c := range cli.Commands {
		names = append(names, c.Name)
	}
	for _, name := range names {
		if !known(name) {
			t.Errorf("%s is not known to the prompt", name)
		}
		var out bytes.Buffer
		err := cli.Run(context.Background(), []string{name, "-h"}, &out)
		if err != nil && strings.Contains(err.Error(), "unknown command") {
			t.Errorf("prompt accepts /%s but the CLI does not: %v", name, err)
		}
	}
	if known("frobnicate") {
		t.Error("frobnicate is known")
	}
}

func TestLineWriter(t *testing.T) {
	lines := make(chan string, 10)
	w := &lineWriter{lines: lines}
	// Lines may be split across writes, or several may come in one.
	w.Write([]byte("fir"))
	w.Write([]byte("st\nsecond\nthi"))
	w.Write([]byte("rd"))
	w.flush()
	close(lines)

	var got []string
	for l := range lines {
		got = append(got, l)
	}
	if want := []string{"first", "second", "third"}; !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}
