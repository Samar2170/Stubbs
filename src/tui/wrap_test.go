package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/x/ansi"
)

// TextareaRows must agree with the textarea package's own wrap result.
// textareaWrap mirrors unexported code in bubbles/textarea, so a dependency
// bump could silently drift; textarea.LineInfo().Height exposes the real
// wrapped height for a single logical line.
func TestTextareaRowsMatchesBubblesWrap(t *testing.T) {
	cases := []string{
		"",
		"aaaaaa",
		"aaaaa ",
		"aaaaa\t",
		"hello world",
		"ababc  world",
		"one two three four five six seven",
		"https://example.com/a/very/long/path/that/keeps/going",
		"中中中中中中中中",
		"word😀word😀word😀",
	}
	for _, width := range []int{3, 5, 6, 7, 10, 20, 58, 78} {
		for _, input := range cases {
			ta := textarea.New()
			ta.Prompt = ""
			ta.ShowLineNumbers = false
			ta.SetWidth(width)
			ta.SetValue(input)
			// The textarea sanitizes its input (tabs become spaces); compare
			// against the value it actually stores and renders.
			value := ta.Value()
			ta.CursorEnd()
			want := ta.LineInfo().Height
			if got := textareaRows(value, ta.Width()); got != want {
				t.Errorf("textareaRows(%q, %d) = %d, bubbles wrap wants %d", value, ta.Width(), got, want)
			}
		}
	}
}

// A long single line must make the composer exactly as tall as the textarea's
// wrapped content; if the estimate is short the textarea scrolls and the first
// line vanishes from view.
func TestComposerFitsWrappedLine(t *testing.T) {
	values := []string{
		strings.Repeat("a", 58), // exactly fills the width
		strings.Repeat("a", 59), // one over
		strings.Repeat("word ", 40),
		"one two three four five six seven eight nine ten alpha beta gamma delta epsilon",
		"https://example.com/" + strings.Repeat("segment/", 20), // unbreakable token
	}
	for _, value := range values {
		m := testModel()
		m.w, m.h = 60, 40
		m.pending = &pendingInput{kind: inTask, reply: make(chan inputResult, 1)}
		m.layout()
		m.ta.SetValue(value)
		m.layout()

		ta := textarea.New()
		ta.Prompt = ""
		ta.ShowLineNumbers = false
		ta.SetWidth(m.ta.Width())
		ta.SetValue(value)
		ta.CursorEnd()
		if want := ta.LineInfo().Height; m.inputH != want {
			t.Errorf("composer height for %d chars = %d, textarea wraps to %d", len(value), m.inputH, want)
		}
		// The box must be tall enough that no wrapped line is dropped.
		if got := len(strings.Split(m.ta.View(), "\n")); got > m.inputH {
			t.Errorf("textarea rendered %d rows but composer only budgets %d", got, m.inputH)
		}
		if !strings.Contains(m.View(), value[:min(10, len(value))]) {
			t.Error("first line of the composer content is hidden from the view")
		}
	}
}

// Status messages (e.g. the /memory listing) used to run off the right edge.
func TestStatusLineWraps(t *testing.T) {
	m := testModel()
	m.w, m.h = 40, 24
	m.layout()
	m.status = strings.Repeat("memory title (id); ", 10)
	m.layout()

	rows := strings.Split(m.statusLine(), "\n")
	if len(rows) != m.statusHeight() {
		t.Fatalf("statusLine rendered %d rows, statusHeight says %d", len(rows), m.statusHeight())
	}
	if len(rows) < 2 {
		t.Fatalf("long status should wrap, got %d row", len(rows))
	}
	for _, l := range rows {
		if ansi.StringWidth(l) > m.w {
			t.Errorf("status row is %d wide, terminal is %d", ansi.StringWidth(l), m.w)
		}
	}
}

// The whole rendered frame must never be wider or taller than the terminal,
// otherwise lines are clipped and become invisible.
func TestViewFitsTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{{20, 8}, {40, 12}, {60, 16}, {80, 24}, {120, 40}}
	values := []string{
		strings.Repeat("a", 240),
		strings.Repeat("The quick brown fox jumps over the lazy dog. ", 8),
		"line one\nline two is much longer and should wrap nicely\nthree",
		"中中中中中中中中中中中中中中中中中中中中中中中中中中中中",
	}
	for _, size := range sizes {
		for _, value := range values {
			m := testModel()
			m.w, m.h = size.w, size.h
			m.pending = &pendingInput{kind: inTask, reply: make(chan inputResult, 1)}
			m.layout()
			m.status = strings.Repeat("a long status entry; ", 6)
			m.ta.SetValue(value)
			m.layout()

			lines := strings.Split(m.View(), "\n")
			if len(lines) > size.h {
				t.Errorf("%dx%d: view has %d rows (%s)", size.w, size.h, len(lines), truncateForLog(value))
			}
			for i, l := range lines {
				if ansi.StringWidth(l) > size.w {
					t.Errorf("%dx%d: row %d is %d wide (%s)", size.w, size.h, i, ansi.StringWidth(l), truncateForLog(value))
				}
			}
		}
	}
}

func truncateForLog(s string) string {
	if len(s) > 40 {
		return fmt.Sprintf("%q…", s[:40])
	}
	return fmt.Sprintf("%q", s)
}

// /memory should put the (possibly long) listing into the scrollable
// transcript rather than the one-line status, so it wraps and stays visible.
func TestMemoryCommandListsInTranscript(t *testing.T) {
	m := testModel()
	m.w, m.h = 50, 12
	m.app = &App{memory: &MemoryHandlers{
		List: func() ([]string, error) {
			return []string{
				"project layout (stubbs-project-layout)",
				"config precedence (stubbs-config-storage-and-precedence)",
				"build and test (stubbs-build-vet-and-test-commands)",
			}, nil
		},
	}}
	m.pending = &pendingInput{kind: inComment, reply: make(chan inputResult, 1)}
	m.layout()

	cmd, handled := m.memoryCommand("/memory")
	if !handled || cmd == nil {
		t.Fatalf("memoryCommand(/memory) = %v, %v; want handled with a fetch cmd", cmd, handled)
	}
	msg, ok := findMsg[memoryListMsg](cmd)
	if !ok || msg.err != nil {
		t.Fatalf("memory list cmd = %+v, %v", msg, ok)
	}
	m.Update(msg)
	if len(m.blocks) != 1 {
		t.Fatalf("memory listing should add one transcript block, got %d", len(m.blocks))
	}
	view := m.View()
	for _, want := range []string{"stubbs-project-layout", "config precedence", "3 memories"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// Every rendered row must still fit the terminal.
	for _, l := range strings.Split(view, "\n") {
		if ansi.StringWidth(l) > m.w {
			t.Errorf("row is %d wide, terminal is %d", ansi.StringWidth(l), m.w)
		}
	}
}
