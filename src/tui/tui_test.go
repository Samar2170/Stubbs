package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"

	"stubbs/src/types"
)

func testModel() *model {
	m := &model{st: newStyles(loadTheme("tokyo"))}
	m.ta = newTextarea()
	return m
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
