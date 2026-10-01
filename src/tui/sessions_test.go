package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"stubbs/src/types"
)

func TestSessionsSlashCommandOpensPicker(t *testing.T) {
	now := time.Now()
	m := testModel()
	m.app = &App{sessions: &SessionHandlers{
		List: func() ([]SessionChoice, error) {
			return []SessionChoice{
				{ID: "2", FirstMessage: "newer task", Updated: now},
				{ID: "1", FirstMessage: "older task", Updated: now.Add(-time.Hour)},
			}, nil
		},
	}}
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}
	m.ta.SetValue("/sessions")

	cmd := m.submitPending()
	if cmd == nil {
		t.Fatal("/sessions should return a fetch command")
	}
	if len(reply) != 0 {
		t.Error("/sessions must not resolve the pending prompt")
	}
	msg, ok := cmd().(sessionsLoadedMsg)
	if !ok {
		t.Fatalf("fetch cmd produced %T, want sessionsLoadedMsg", cmd())
	}
	m.Update(msg)
	if m.dlg == nil || m.dlg.kind != dlgSessions {
		t.Fatalf("expected sessions dialog, got %+v", m.dlg)
	}
	if len(m.dlg.options) != 2 {
		t.Fatalf("dialog has %d options, want 2", len(m.dlg.options))
	}
}

func TestSessionsDialogShowsFirstMessageNewestFirst(t *testing.T) {
	now := time.Now()
	d := newSessionsDialog([]SessionChoice{
		{ID: "2", FirstMessage: "newer task", Updated: now},
		{ID: "1", FirstMessage: "older task", Updated: now.Add(-48 * time.Hour)},
	})
	view := d.render(80, newStyles(loadTheme("tokyo")))
	for _, want := range []string{"newer task", "older task", "open a session"} {
		if !strings.Contains(view, want) {
			t.Errorf("sessions dialog missing %q", want)
		}
	}
	// The backend order is preserved (newest first).
	if d.options[0].text != "2" {
		t.Errorf("first option = %q, want session 2", d.options[0].text)
	}
}

func TestSessionsDialogSelectResumes(t *testing.T) {
	var opened string
	m := testModel()
	m.app = &App{sessions: &SessionHandlers{
		Open: func(id string) ([]types.Message, error) {
			opened = id
			return []types.Message{
				{Role: types.RoleSystem, Content: "sys"},
				{Role: types.RoleUser, Content: "old task"},
				{Role: types.RoleAssistant, Content: "old reply"},
			}, nil
		},
	}}
	m.dlg = newSessionsDialog([]SessionChoice{{ID: "7", FirstMessage: "old task", Updated: time.Now()}})

	cmd := m.pickDialogOption()
	if m.dlg != nil {
		t.Error("picking a session should close the dialog")
	}
	if cmd == nil {
		t.Fatal("picking a session should return an open command")
	}
	msg, ok := cmd().(sessionOpenedMsg)
	if !ok {
		t.Fatalf("open cmd produced %T, want sessionOpenedMsg", cmd())
	}
	if msg.err != nil || msg.id != "7" {
		t.Fatalf("open msg = %+v", msg)
	}
	if opened != "7" {
		t.Errorf("Open called with %q, want 7", opened)
	}
	m.Update(msg)
	if len(m.blocks) == 0 {
		t.Fatal("resumed session should replay transcript blocks")
	}
}

func TestSessionOpenErrorSetsStatus(t *testing.T) {
	m := testModel()
	m.app = &App{sessions: &SessionHandlers{
		Open: func(id string) ([]types.Message, error) {
			return nil, errors.New("boom")
		},
	}}
	m.dlg = newSessionsDialog([]SessionChoice{{ID: "7", FirstMessage: "x", Updated: time.Now()}})
	cmd := m.pickDialogOption()
	m.Update(cmd())
	if !m.statusErr || !strings.Contains(m.status, "boom") {
		t.Fatalf("open error should surface in status, got %q (err=%v)", m.status, m.statusErr)
	}
}

func TestSessionReplayKeepsPendingPrompt(t *testing.T) {
	m := testModel()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}
	m.replaySession([]types.Message{
		{Role: types.RoleUser, Content: "hello"},
		{Role: types.RoleAssistant, Content: "hi"},
	})
	if m.pending == nil || m.pending.kind != inTask {
		t.Fatal("replay must keep the task prompt so a follow-up can continue it")
	}
	// The user message and assistant reply should both be shown.
	var user, assistant bool
	for _, b := range m.blocks {
		switch b := b.(type) {
		case userBlock:
			user = b.text == "hello"
		case assistantBlock:
			assistant = b.content == "hi"
		}
	}
	if !user || !assistant {
		t.Errorf("replay missing blocks: user=%v assistant=%v", user, assistant)
	}
}

func TestSessionsFilter(t *testing.T) {
	d := newSessionsDialog([]SessionChoice{
		{ID: "1", FirstMessage: "fix the parser", Updated: time.Now()},
		{ID: "2", FirstMessage: "add tests", Updated: time.Now()},
	})
	d.filter = "parser"
	d.applyFilter()
	if len(d.options) != 1 || d.options[0].text != "1" {
		t.Fatalf("filter 'parser' = %+v", d.options)
	}
}
