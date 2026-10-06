package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"stubbs/src/agent"
	"stubbs/src/types"
)

func testModel() *model {
	m := &model{st: newStyles(loadTheme("tokyo"))}
	m.ta = newTextarea()
	return m
}

// runCmd executes a command and flattens any tea.BatchMsg into the messages
// produced by its sub-commands. It lets tests work with handlers that batch a
// spinner tick alongside their real command.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmd(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// findMsg runs cmd and returns the first message of type T it produces.
func findMsg[T any](cmd tea.Cmd) (T, bool) {
	var zero T
	for _, msg := range runCmd(cmd) {
		if v, ok := msg.(T); ok {
			return v, true
		}
	}
	return zero, false
}

// TestAwaitReturnsWhenAppClosed guards against the freeze where main blocked
// on wg.Wait() forever because a pending Ask* never resolved after the TUI
// program stopped (e.g. ctrl-c delivered as SIGINT).
func TestAwaitReturnsWhenAppClosed(t *testing.T) {
	a := &App{closed: make(chan struct{})}
	close(a.closed)
	if _, err := a.await(make(chan inputResult, 1)); !errors.Is(err, agent.ErrInterrupted) {
		t.Fatalf("await after program stop = %v, want ErrInterrupted", err)
	}
}

func TestSpinnerOnlyTicksWhileBusy(t *testing.T) {
	m := testModel()
	_, cmd := m.Update(statusMsg("waiting for LLM..."))
	if cmd == nil || !m.spinning {
		t.Fatal("a busy status should start the spinner")
	}
	m.Update(copiedMsg(1)) // clears working
	if m.busy() {
		t.Fatal("a clipboard status must not count as busy")
	}
	_, cmd = m.Update(spinner.TickMsg{})
	if cmd != nil || m.spinning {
		t.Fatal("an idle spinner tick should stop and schedule nothing")
	}
}

func TestAppendBlockRendersIncrementally(t *testing.T) {
	m := testModel()
	m.vp.Width, m.vp.Height = 80, 20

	m.appendBlock(userBlock{text: "hello"})
	if len(m.tLines) == 0 {
		t.Fatal("appendBlock should render its lines without a full re-render")
	}
	first := len(m.tLines)

	m.appendBlock(infoBlock{text: "world"})
	if len(m.tLines) <= first {
		t.Fatal("a second append should extend the transcript")
	}
	if len(m.rows) != 2 {
		t.Fatalf("rows = %v, want 2 entries", m.rows)
	}
	if m.rows[1] <= m.rows[0] {
		t.Fatalf("rows should accumulate across blocks: %v", m.rows)
	}
}

func TestFinishSummaryInterrupted(t *testing.T) {
	got, ok := finishSummary(agent.ErrInterrupted)
	if ok || got != "Run interrupted." {
		t.Fatalf("finishSummary(ErrInterrupted) = %q, %v; want %q, false", got, ok, "Run interrupted.")
	}
}

func TestLoadTheme(t *testing.T) {
	if loadTheme("tokyo").primary != tokyo.primary {
		t.Error("expected tokyo palette by default")
	}
	if loadTheme("system").primary != system.primary {
		t.Errorf(`loadTheme("system") = %v, want ANSI 4`, loadTheme("system").primary)
	}
	if loadTheme("bogus").primary != tokyo.primary {
		t.Error("unknown themes should fall back to tokyo")
	}
}

func TestToolBlockShowsOutputByDefault(t *testing.T) {
	s := newStyles(loadTheme("tokyo"))
	b := toolBlock{
		name:     "bash",
		cmd:      "ls -la",
		out:      types.ExecutionOutput{Output: "out\nout2", Code: 0, Duration: 120 * time.Millisecond},
		expanded: true,
	}
	view := b.render(80, s)
	if !strings.Contains(view, "out2") {
		t.Error("tool block should show output by default")
	}
	if !strings.Contains(view, "exit 0") || !strings.Contains(view, "bash") {
		t.Errorf("summary missing details: %q", view)
	}
}

func TestToolBlockCollapsedHidesOutput(t *testing.T) {
	s := newStyles(loadTheme("tokyo"))
	b := toolBlock{
		name: "bash",
		out:  types.ExecutionOutput{Output: "line1\nline2", Code: 0},
	}
	view := b.render(80, s)
	if strings.Contains(view, "line1") {
		t.Errorf("collapsed tool block must not show output: %q", view)
	}
}

func TestClipKeepsHeadAndTail(t *testing.T) {
	in := strings.Repeat("x\n", 100)
	out := clip(in, 10)
	if got := len(strings.Split(out, "\n")); got != 10 {
		t.Fatalf("clip(100 lines, 10) produced %d lines, want 10", got)
	}
	if !strings.Contains(out, "lines hidden") {
		t.Error("clip should mark hidden lines")
	}
	if clip("short", 10) != "short" {
		t.Error("clip should pass short strings through unchanged")
	}
}

func TestAssistantBlockHeader(t *testing.T) {
	s := newStyles(loadTheme("tokyo"))
	b := assistantBlock{step: 3, cost: 0.5, content: "hello", calls: []string{"ls"}}
	view := b.render(80, s)
	for _, want := range []string{"stubbs", "step 3", "$0.5000", "hello", "→ ls"} {
		if !strings.Contains(view, want) {
			t.Errorf("assistant block missing %q", want)
		}
	}
}

func TestConfirmDialogApproveResolvesEmptyText(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.dlg = &dialog{kind: dlgConfirm, options: confirmOptions(), reply: reply}
	m.pickDialogOption() // selected defaults to 0 = approve
	res := <-reply
	if res.text != "" || res.interrupted || res.aborted {
		t.Errorf("approve should resolve empty text, got %+v", res)
	}
	if m.dlg != nil {
		t.Error("dialog should be closed after resolve")
	}
}

func TestConfirmDialogYoloResolvesSlashY(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.dlg = &dialog{kind: dlgConfirm, options: confirmOptions(), reply: reply, selected: 1}
	m.pickDialogOption()
	if res := <-reply; res.text != "/y" {
		t.Errorf("yolo option resolved %q, want /y", res.text)
	}
}

func TestConfirmDialogRejectSwapsToComposer(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.dlg = &dialog{kind: dlgConfirm, options: confirmOptions(), reply: reply, selected: 2}
	m.pickDialogOption()
	if m.dlg != nil {
		t.Fatal("dialog should close when swapping to reject composer")
	}
	if m.pending == nil || m.pending.kind != inReject {
		t.Fatalf("expected inReject pending, got %+v", m.pending)
	}
	if len(reply) != 0 {
		t.Error("reject swap must not resolve yet")
	}
	m.ta.SetValue("nope")
	m.submitPending()
	res := <-reply
	if res.text != "nope" {
		t.Errorf("reject composer resolved %q, want 'nope'", res.text)
	}
}

func TestExitDialogNewTaskSwapsComposer(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.dlg = &dialog{kind: dlgExit, options: exitOptions(), reply: reply, selected: 1}
	m.pickDialogOption()
	if m.pending == nil || m.pending.kind != inTask {
		t.Fatalf("expected inTask pending, got %+v", m.pending)
	}
	m.ta.SetValue("do another thing")
	m.submitPending()
	if res := <-reply; res.text != "do another thing" {
		t.Errorf("new task resolved %q", res.text)
	}
}

func TestHelpSlashCommandOpensOverlay(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inComment, reply: reply}
	m.ta.SetValue("/h")
	m.submitPending()
	if m.dlg == nil || m.dlg.kind != dlgHelp {
		t.Fatalf("expected help overlay, got %+v", m.dlg)
	}
	if len(reply) != 0 {
		t.Error("/h must not resolve the pending prompt")
	}
}

func TestLimitsValidation(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inLimits, reply: reply}

	m.ta.SetValue("garbage")
	m.submitPending()
	if !m.statusErr || len(reply) != 0 {
		t.Error("invalid limits should set status error and not resolve")
	}

	m.ta.SetValue("24 5")
	m.submitPending()
	res := <-reply
	if res.text != "24 5" {
		t.Errorf("valid limits resolved %q", res.text)
	}
}

func TestLimitsQEndsRun(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inLimits, reply: reply}
	m.ta.SetValue("q")
	m.submitPending()
	res := <-reply
	if !res.cancelled {
		t.Error("'q' should cancel the run")
	}
}

func TestDialogRenderContainsOptions(t *testing.T) {
	s := newStyles(loadTheme("tokyo"))
	d := &dialog{kind: dlgConfirm, commands: []string{"rm -rf /"}, options: confirmOptions()}
	view := d.render(80, s)
	for _, want := range []string{"approve commands?", "rm -rf /", "Approve and run", "yolo"} {
		if !strings.Contains(view, want) {
			t.Errorf("confirm dialog missing %q", want)
		}
	}
	if help := newHelpDialog().render(80, s); !strings.Contains(help, "ctrl+o") {
		t.Error("help overlay missing keybinds")
	}
}

func TestTextareaStillWorksAfterResolve(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}
	m.ta = textarea.New()
	m.ta.SetValue("task")
	m.submitPending()
	if res := <-reply; res.text != "task" {
		t.Errorf("task resolved %q", res.text)
	}
	if m.pending != nil || m.status != "" {
		t.Error("resolve should clear pending state")
	}
}

func TestSubmitEchoesUserMessage(t *testing.T) {
	for _, kind := range []inputKind{inTask, inComment, inReject} {
		m := testModel()
		reply := make(chan inputResult, 1)
		m.pending = &pendingInput{kind: kind, reply: reply}
		m.ta.SetValue("my input")
		m.submitPending()
		<-reply
		var found bool
		for _, b := range m.blocks {
			if ub, ok := b.(userBlock); ok && ub.text == "my input" {
				found = true
			}
		}
		if !found {
			t.Errorf("kind %d: user message not echoed to transcript", kind)
		}
	}
}

func TestSubmitDoesNotEchoLimitsOrCommands(t *testing.T) {
	for kind, val := range map[inputKind]string{inLimits: "24 5", inCommand: "some text"} {
		m := testModel()
		reply := make(chan inputResult, 1)
		m.pending = &pendingInput{kind: kind, reply: reply}
		m.ta.SetValue(val)
		m.submitPending()
		<-reply
		if len(m.blocks) != 0 {
			t.Errorf("kind %d: should not echo to transcript", kind)
		}
	}
}

func fakeLines(n int) []tLine {
	lines := make([]tLine, n)
	for i := range lines {
		lines[i] = tLine{plain: "text " + strings.Repeat("x", i), ansi: "text"}
	}
	return lines
}

func TestSelRange(t *testing.T) {
	m := testModel()
	m.tLines = fakeLines(10)
	m.selAnchor, m.selHead = -1, -1

	if lo, hi := m.selRange(); lo <= hi {
		t.Error("no selection should give empty range")
	}

	m.selAnchor, m.selHead = 3, 7
	if lo, hi := m.selRange(); lo != 3 || hi != 7 {
		t.Errorf("forward range = %d..%d, want 3..7", lo, hi)
	}

	m.selAnchor, m.selHead = 7, 3
	if lo, hi := m.selRange(); lo != 3 || hi != 7 {
		t.Errorf("reverse range = %d..%d, want 3..7", lo, hi)
	}

	m.selAnchor, m.selHead = 8, 99
	if _, hi := m.selRange(); hi != 9 {
		t.Errorf("clamped hi = %d, want 9", hi)
	}
}

func TestSelectedTextTrimsAndJoins(t *testing.T) {
	m := testModel()
	m.tLines = []tLine{
		{plain: ""},
		{plain: "first line        ", ansi: "x"},
		{plain: "second line", ansi: "x"},
		{plain: ""},
		{plain: "", ansi: "x"},
	}
	got := m.selectedText(0, 4)
	if got != "first line\nsecond line" {
		t.Errorf("selectedText = %q, want %q", got, "first line\nsecond line")
	}
}

func TestComposerAutoGrow(t *testing.T) {
	m := testModel()
	m.w, m.h = 80, 24
	m.layout()

	if m.inputH != 1 {
		t.Fatalf("empty composer height = %d, want 1", m.inputH)
	}

	m.ta.SetValue("one\ntwo\nthree")
	m.layout()
	if m.inputH != 3 {
		t.Errorf("multiline composer height = %d, want 3", m.inputH)
	}

	// A long line wraps and needs an extra row (composer width = 80-2).
	m.ta.SetValue(strings.Repeat("a", 100))
	m.layout()
	if m.inputH != 2 {
		t.Errorf("wrapped composer height = %d, want 2", m.inputH)
	}

	// The expanded flag (ctrl+e, /m) forces a minimum of 8 rows.
	m.expanded = true
	m.ta.SetValue("")
	m.layout()
	if m.inputH != 8 {
		t.Errorf("expanded composer height = %d, want 8", m.inputH)
	}
	m.expanded = false

	// Tiny terminals cap the composer so every other row (header, status and
	// at least one transcript row) still fits on screen.
	m.h = 8
	m.ta.SetValue(strings.Repeat("x\n", 50))
	m.layout()
	if m.inputH > 4 {
		t.Errorf("capped composer height = %d, want <= 4", m.inputH)
	}
	if got := len(strings.Split(m.View(), "\n")); got > m.h {
		t.Errorf("view is %d rows tall, want <= %d", got, m.h)
	}
}

func TestUpdateTAGrowsAndShrinksBox(t *testing.T) {
	m := testModel()
	m.w, m.h = 80, 24
	m.layout()

	m.updateTA(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb")})
	if m.inputH != 2 {
		t.Errorf("height after newline = %d, want 2", m.inputH)
	}

	m.ta.Reset()
	m.layout()
	if m.inputH != 1 {
		t.Errorf("height after reset = %d, want 1", m.inputH)
	}
}

func TestMouseClickTogglesToolBlock(t *testing.T) {
	m := testModel()
	m.vp.Width, m.vp.Height = 80, 20
	m.appendBlock(userBlock{text: "hi"})
	m.appendBlock(toolBlock{name: "bash", expanded: true})
	m.dirty = true
	m.renderTranscript()

	// Click on the tool block's first line (row 3: 1 user line + blank + tool line).
	m.handleMouse(tea.MouseMsg{
		X: 5, Y: 4, Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress, Type: tea.MouseMotion,
	})
	m.handleMouse(tea.MouseMsg{
		X: 5, Y: 4, Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease, Type: tea.MouseMotion,
	})
	if m.selecting {
		t.Error("selection should end on release")
	}
	if len(m.blocks) != 2 {
		t.Fatalf("unexpected block count %d", len(m.blocks))
	}
	tb, ok := m.blocks[1].(toolBlock)
	if !ok || tb.expanded {
		t.Errorf("click should have collapsed the tool block, got %+v", tb)
	}
}

func TestEscapeQuitsWhenDone(t *testing.T) {
	m := testModel()
	m.done = true
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc while the run is done should quit the program")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("esc while done produced %T, want tea.QuitMsg", cmd())
	}
}

func TestComposerHasNoCursorLineBackground(t *testing.T) {
	ta := newTextarea()
	if _, ok := ta.FocusedStyle.CursorLine.GetBackground().(lipgloss.NoColor); !ok {
		t.Error("composer focused cursor line must not paint a background")
	}
	if _, ok := ta.BlurredStyle.CursorLine.GetBackground().(lipgloss.NoColor); !ok {
		t.Error("composer blurred cursor line must not paint a background")
	}
}

func TestResetPromptRefocusesComposer(t *testing.T) {
	m := testModel()
	m.ta.Blur()
	m.resetPrompt()
	if !m.ta.Focused() {
		t.Error("resetPrompt should refocus the composer")
	}
}

func TestBusyTracksWorkingNotStatus(t *testing.T) {
	m := testModel()
	m.Update(statusMsg("waiting for LLM..."))
	if !m.busy() {
		t.Error("an agent status should count as busy")
	}
	m.Update(copiedMsg(2))
	if m.busy() {
		t.Error("a clipboard status must not count as busy")
	}
}

func TestMouseDragSelectionCopies(t *testing.T) {
	m := testModel()
	m.vp.Width, m.vp.Height = 80, 20
	m.appendBlock(userBlock{text: "line one"})
	m.appendBlock(infoBlock{text: "line two"})
	m.dirty = true
	m.renderTranscript()

	m.handleMouse(tea.MouseMsg{
		X: 0, Y: 2, Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress, Type: tea.MouseMotion,
	})
	m.handleMouse(tea.MouseMsg{
		X: 0, Y: 3, Button: tea.MouseButtonLeft,
		Action: tea.MouseActionMotion, Type: tea.MouseMotion,
	})
	cmd := m.handleMouse(tea.MouseMsg{
		X: 0, Y: 3, Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease, Type: tea.MouseMotion,
	})
	if cmd == nil {
		t.Fatal("drag-release should return a copy command")
	}
	for _, b := range m.blocks {
		if tb, ok := b.(toolBlock); ok && !tb.expanded {
			t.Error("drag must not toggle tool blocks")
		}
	}
	if m.selAnchor < 0 {
		t.Error("selection highlight should persist after copy")
	}
	// A keypress clears the highlight.
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.selAnchor >= 0 {
		t.Error("keypress should clear selection")
	}
}

func TestModelsSlashCommandOpensPicker(t *testing.T) {
	m := testModel()
	m.app = &App{modelsFn: func() ([]ModelChoice, error) {
		return []ModelChoice{{ID: "a/one", Name: "One"}, {ID: "b/two", Name: "Two"}}, nil
	}}
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inComment, reply: reply}
	m.ta.SetValue("/models")

	cmd := m.submitPending()
	if cmd == nil {
		t.Fatal("/models should return a fetch command")
	}
	if len(reply) != 0 {
		t.Error("/models must not resolve the pending prompt")
	}
	msg, ok := cmd().(modelsLoadedMsg)
	if !ok {
		t.Fatalf("fetch cmd produced %T, want modelsLoadedMsg", cmd())
	}
	m.Update(msg)
	if m.dlg == nil || m.dlg.kind != dlgModels {
		t.Fatalf("expected models dialog, got %+v", m.dlg)
	}
	if len(m.dlg.options) != 2 {
		t.Fatalf("dialog has %d options, want 2", len(m.dlg.options))
	}
}

func TestModelsDialogFilter(t *testing.T) {
	d := newModelsDialog([]ModelChoice{
		{ID: "anthropic/claude", Name: "Claude"},
		{ID: "openai/gpt", Name: "GPT"},
		{ID: "meta/llama", Name: ""},
	})
	d.filter = "cla"
	d.applyFilter()
	if len(d.options) != 1 || d.options[0].text != "anthropic/claude" {
		t.Fatalf("filter 'cla' = %+v", d.options)
	}
	d.filter = ""
	d.applyFilter()
	if len(d.options) != 3 {
		t.Fatalf("cleared filter should restore all options, got %d", len(d.options))
	}
}

func TestModelsDialogSelectSwitches(t *testing.T) {
	var switched string
	m := testModel()
	m.app = &App{switchFn: func(id string) error {
		switched = id
		return nil
	}}
	m.dlg = newModelsDialog([]ModelChoice{{ID: "a/one", Name: "One"}, {ID: "b/two", Name: "Two"}})
	m.dlg.selected = 1

	cmd := m.pickDialogOption()
	if m.dlg != nil {
		t.Error("picking a model should close the dialog")
	}
	if cmd == nil {
		t.Fatal("picking a model should return a switch command")
	}
	res, ok := findMsg[modelSwitchedMsg](cmd)
	if !ok || res.err != nil || res.id != "b/two" {
		t.Fatalf("switch cmd = %+v, %v", res, ok)
	}
	if switched != "b/two" {
		t.Errorf("switchFn called with %q, want b/two", switched)
	}
}
