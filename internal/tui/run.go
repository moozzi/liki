package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"liki/internal/cli"
)

// outputMsg carries one line printed by a running command; next waits for the following one.
type outputMsg struct {
	line string
	next tea.Cmd
}

// doneMsg reports that the running command finished.
type doneMsg struct{ err error }

// start runs a CLI command in the background, streaming its output above the prompt.
func (m *model) start(label string, args []string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.running, m.cancel = label, cancel

	lines := make(chan string)
	done := make(chan error, 1)
	w := &lineWriter{lines: lines}
	go func() {
		err := cli.Run(ctx, args, w)
		w.flush()
		close(lines)
		if ctx.Err() != nil && err != nil {
			err = context.Canceled
		}
		cancel()
		done <- err
	}()
	return tea.Batch(m.spinner.Tick, waitOutput(lines, done))
}

func waitOutput(lines <-chan string, done <-chan error) tea.Cmd {
	return func() tea.Msg {
		if line, ok := <-lines; ok {
			return outputMsg{line: line, next: waitOutput(lines, done)}
		}
		return doneMsg{err: <-done}
	}
}

// lineWriter sends every complete line written to it on a channel.
type lineWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	lines chan<- string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.buf.Next(i + 1))
		w.lines <- strings.TrimSuffix(line, "\n")
	}
}

// Columns makes commands mark right columns instead of aligning them, so
// rows are aligned for the current width, and again after a resize.
func (w *lineWriter) Columns() bool { return true }

// Interactive tells commands to suggest /commands in their hints.
func (w *lineWriter) Interactive() bool { return true }

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.lines <- w.buf.String()
		w.buf.Reset()
	}
}

// splitArgs splits a command line into arguments like a shell would:
// on spaces, honoring 'single' and "double" quotes and backslash escapes.
func splitArgs(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inArg   bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inArg = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inArg = r, true
		case unicode.IsSpace(r):
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
