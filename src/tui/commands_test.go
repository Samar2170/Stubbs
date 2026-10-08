package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typeKeys feeds runes through the real key path so the composer and the menu
// stay in step the way they do at runtime.
func typeKeys(m *model, s string) {
	for _, r := range s {
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// memoryModel is a model mid-prompt with the memory handlers wired up, so the
// menu is live and the memory slash commands are offered.
func memoryModel() *model {
	m := testModel()
	m.app = &App{memory: &MemoryHandlers{}}
	m.pending = &pendingInput{kind: inComment, reply: make(chan inputResult, 1)}
	return m
}

func TestMenuOpensOnSlash(t *testing.T) {
	m := memoryModel()
	typeKeys(m, "/")
	if !m.menu.open {
		t.Fatal("typing '/' should open the command menu")
	}
	if got := len(m.menu.options); got != len(m.commands()) {
		t.Fatalf("menu shows %d commands, want %d", got, len(m.commands()))
	}
	for _, want := range []string{"/h", "/models", "/memory"} {
		var found bool
		for _, o := range m.menu.options {
			if o.name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("menu missing %s", want)
		}
	}
}

func TestMenuHidesWithoutMemoryHandlers(t *testing.T) {
	m := testModel()
	typeKeys(m, "/")
	for _, o := range m.menu.options {
		switch o.name {
		case "/remember", "/forget", "/memory", "/map":
			t.Errorf("%s should be hidden when memory is not wired up", o.name)
		}
	}

	m.app = &App{memory: &MemoryHandlers{List: func() ([]string, error) { return nil, nil }}}
	m.menu.refresh(m.ta.Value(), m.commands())
	if len(m.menu.options) != len(m.commands()) {
		t.Fatal("wired memory handlers should expose the memory commands")
	}
	var found bool
	for _, o := range m.menu.options {
		if o.name == "/remember" {
			found = true
		}
	}
	if !found {
		t.Error("menu missing /remember once memory is wired up")
	}
}

func TestMenuFiltersAsYouType(t *testing.T) {
	m := memoryModel()
	typeKeys(m, "/rem")
	if !m.menu.open {
		t.Fatalf("menu should stay open for %q", m.ta.Value())
	}
	if len(m.menu.options) != 1 || m.menu.options[0].name != "/remember" {
		t.Fatalf("filter '/rem' = %+v", m.menu.options)
	}
}

func TestMenuHiddenWithoutPendingPrompt(t *testing.T) {
	m := testModel()
	typeKeys(m, "/h")
	if m.menu.open {
		t.Error("the menu should only appear while a prompt is waiting")
	}
}

func TestMenuClosesOnArgumentOrSpace(t *testing.T) {
	for _, text := range []string{"/h ", "/h now", "/rem" + "ember x", "hi", "/nosuch"} {
		m := memoryModel()
		m.ta.SetValue(text)
		m.syncMenu()
		if m.menu.open {
			t.Errorf("menu should be closed for %q, got %+v", text, m.menu.options)
		}
	}
}

func TestMenuClosesWhenTextCleared(t *testing.T) {
	m := memoryModel()
	typeKeys(m, "/h")
	if !m.menu.open {
		t.Fatal("menu should be open")
	}
	m.ta.Reset()
	m.syncMenu()
	if m.menu.open {
		t.Error("clearing the composer should close the menu")
	}
}

func TestMenuArrowKeysMoveSelection(t *testing.T) {
	m := memoryModel()
	typeKeys(m, "/")
	n := len(m.menu.options)
	m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.menu.selected != 1 {
		t.Fatalf("down = %d, want 1", m.menu.selected)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if m.menu.selected != 0 {
		t.Fatalf("up = %d, want 0", m.menu.selected)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if m.menu.selected != n-1 {
		t.Fatalf("up should wrap to %d, got %d", n-1, m.menu.selected)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.menu.selected != 0 {
		t.Fatalf("down should wrap to 0, got %d", m.menu.selected)
	}
}

func TestMenuTabCompletesWithoutSubmitting(t *testing.T) {
	m := memoryModel()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inComment, reply: reply}
	typeKeys(m, "/re")
	if len(m.menu.options) != 1 {
		t.Fatalf("expected /remember only, got %+v", m.menu.options)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.ta.Value(); got != "/remember " {
		t.Fatalf("tab completed %q, want %q", got, "/remember ")
	}
	if len(reply) != 0 {
		t.Error("completing an argument-taking command must not submit")
	}
	if m.menu.open {
		t.Error("the menu should close once the argument hint is inserted")
	}
}

func TestMenuEnterSubmitsArglessCommand(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inComment, reply: reply}
	typeKeys(m, "/h")
	if !m.menu.open {
		t.Fatal("menu should be open")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.dlg == nil || m.dlg.kind != dlgHelp {
		t.Fatalf("enter on /h should open help, got %+v", m.dlg)
	}
	if len(reply) != 0 {
		t.Error("TUI-side commands must not resolve the pending prompt")
	}
}

func TestMenuEnterAcceptsThenSubmitsSelection(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inComment, reply: reply}
	typeKeys(m, "/mo")
	// /models is the only match.
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.status != "loading models…" {
		t.Fatalf("enter on /models should start the model fetch, status = %q", m.status)
	}
}

func TestMenuEscClosesWithoutInterrupting(t *testing.T) {
	m := memoryModel()
	m.app.interrupt = func() { t.Error("esc must not interrupt while the menu is open") }
	typeKeys(m, "/h")
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.menu.open {
		t.Error("esc should close the menu")
	}
	if m.ta.Value() != "/h" {
		t.Errorf("esc should keep the typed text, got %q", m.ta.Value())
	}
}

func TestMenuHiddenWhileDoneOrDialog(t *testing.T) {
	m := memoryModel()
	typeKeys(m, "/h")
	if !m.menu.open {
		t.Fatal("menu should open")
	}
	m.done = true
	m.syncMenu()
	if m.menu.open {
		t.Error("a finished run should not show the menu")
	}

	m.done = false
	m.dlg = newHelpDialog()
	m.syncMenu()
	if m.menu.open {
		t.Error("an open dialog should hide the menu")
	}
}

func TestMenuInLimitsPromptStaysHidden(t *testing.T) {
	m := memoryModel()
	m.pending = &pendingInput{kind: inLimits, reply: make(chan inputResult, 1)}
	typeKeys(m, "/h")
	if m.menu.open {
		t.Error("the limits prompt must not offer slash commands")
	}
}

func TestMenuReservesLayoutRows(t *testing.T) {
	m := memoryModel()
	m.w, m.h = 80, 30
	m.layout()
	plain := m.vp.Height

	typeKeys(m, "/")
	m.layout()
	if m.menuHeight() == 0 {
		t.Fatal("menu should occupy rows while open")
	}
	if m.vp.Height >= plain {
		t.Errorf("menu should shrink the transcript: %d -> %d", plain, m.vp.Height)
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.layout()
	if m.vp.Height != plain {
		t.Errorf("closing the menu should restore the viewport: %d, want %d", m.vp.Height, plain)
	}
}

func TestMenuHeightCappedOnTinyTerminal(t *testing.T) {
	m := memoryModel()
	m.w, m.h = 80, 12
	m.layout()
	typeKeys(m, "/")
	m.layout()
	if h := m.menuHeight(); h > m.h {
		t.Errorf("menu height %d exceeds terminal height %d", h, m.h)
	}
	if m.vp.Height < 1 {
		t.Errorf("viewport should keep at least one row, got %d", m.vp.Height)
	}
}

func TestRenderMenuHighlightsSelection(t *testing.T) {
	m := memoryModel()
	m.w, m.h = 100, 30
	m.layout()
	typeKeys(m, "/")
	m.menu.selected = 1
	view := m.renderMenu()
	if !strings.Contains(view, "❯") {
		t.Error("menu should mark the selected row")
	}
	for _, want := range []string{"/h", "/models", "show help"} {
		if !strings.Contains(view, want) {
			t.Errorf("menu missing %q:\n%s", want, view)
		}
	}
	if got := strings.Count(view, "\n") + 1; got != m.menuHeight() {
		t.Errorf("rendered menu is %d rows, layout reserved %d", got, m.menuHeight())
	}
}

func TestMenuShowsInView(t *testing.T) {
	m := memoryModel()
	m.w, m.h = 80, 30
	m.layout()
	typeKeys(m, "/")
	if !strings.Contains(m.View(), "/models") {
		t.Error("the view should include the command menu")
	}
}

func TestCommandQuery(t *testing.T) {
	cases := map[string]string{
		"/":         "/",
		"/rem":      "/rem",
		"/h ":       "",
		"/remember": "/remember",
		"text":      "",
		" /h":       "",
		"/h\nx":     "",
	}
	for in, want := range cases {
		if got := commandQuery(in); got != want {
			t.Errorf("commandQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMenuWindowsAroundSelection(t *testing.T) {
	m := memoryModel()
	m.w, m.h = 80, 24
	m.layout()
	typeKeys(m, "/")
	if len(m.menu.options) <= maxCommandRows {
		t.Skipf("only %d commands; nothing to window", len(m.menu.options))
	}
	rows, off := m.menu.visible(m.menuLimit())
	if len(rows) != m.menuLimit() {
		t.Fatalf("window has %d rows, want %d", len(rows), m.menuLimit())
	}
	if off != 0 {
		t.Fatalf("window should start at 0, got %d", off)
	}
	m.menu.move(len(m.menu.options) - 1) // last command
	rows, off = m.menu.visible(m.menuLimit())
	if got := off + len(rows); got != len(m.menu.options) {
		t.Fatalf("window should end at %d, got %d", len(m.menu.options), got)
	}
	if m.menu.selected < off || m.menu.selected >= off+len(rows) {
		t.Errorf("selection %d outside window %d..%d", m.menu.selected, off, off+len(rows)-1)
	}
}

func TestMenuSelectionResetsOnNewQuery(t *testing.T) {
	m := memoryModel()
	typeKeys(m, "/")
	m.menu.move(3)
	if m.menu.selected == 0 {
		t.Fatal("selection should have moved")
	}
	typeKeys(m, "m")
	if m.menu.selected != 0 {
		t.Errorf("a changed query should reset the selection, got %d", m.menu.selected)
	}
}
