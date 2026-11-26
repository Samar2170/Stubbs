package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// e2eModel builds a model the way the running app does: a WindowSizeMsg
// drives layout(), the transcript overflows the viewport, and every mouse
// message below flows through m.Update() — not through handleMouse directly,
// which the other selection tests use. The messages carry the exact field
// values bubbletea v1.3.10 decodes from gnome-terminal SGR reports: (1,1) is
// the top-left cell, bubbletea subtracts one, press/motion/release arrive as
// Action press/motion/release with Button left.
func e2eModel(t *testing.T) *model {
	t.Helper()
	m := testModel()
	m.selAnchor, m.selHead = -1, -1
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := 0; i < 40; i++ {
		m.appendBlock(infoBlock{text: "line " + strings.Repeat("x", 8)})
	}
	m.dirty = true
	m.renderTranscript()
	m.layout()
	if m.vp.TotalLineCount() <= m.vp.Height {
		t.Fatalf("transcript does not overflow: %d lines, height %d", m.vp.TotalLineCount(), m.vp.Height)
	}
	return m
}

// The full drag path through Update: press below the header, drag down, and
// the middle line of the span must show up highlighted in the rendered view;
// release must yield a copy command.
func TestMouseSelectEndToEndThroughUpdate(t *testing.T) {
	m := e2eModel(t)

	top := m.headerH // first transcript row on screen
	_, _ = m.Update(tea.MouseMsg{X: 0, Y: top, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.selecting {
		t.Fatalf("press did not start a selection (headerH=%d vp.Height=%d vp.YOffset=%d tLines=%d)",
			m.headerH, m.vp.Height, m.vp.YOffset, len(m.tLines))
	}
	if m.selHead != m.vp.YOffset {
		t.Fatalf("press anchor = %d, want viewport top %d", m.selHead, m.vp.YOffset)
	}

	_, _ = m.Update(tea.MouseMsg{X: 5, Y: top + 2, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	if !m.moved {
		t.Fatal("drag did not mark the selection as moved")
	}

	// The line between anchor and head spans the full width, so it must be
	// highlighted in full.
	start, end, ok := m.orderedSelection()
	if !ok || end.line-start.line < 2 {
		t.Fatalf("unexpected selection: %+v..%+v ok=%v", start, end, ok)
	}
	want := m.st.selLine.Render(m.tLines[start.line+1].plain)
	if view := m.vp.View(); !strings.Contains(view, want) {
		t.Fatalf("middle-line highlight %q missing from rendered view:\n%s", want, view)
	}

	_, cmd := m.Update(tea.MouseMsg{X: 5, Y: top + 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if cmd == nil {
		t.Fatal("release through Update returned no copy command")
	}
}

// The agent keeps streaming while the user drags: blocks arriving and the
// transcript re-rendering between press and release must not cancel the
// selection or swallow the copy.
func TestMouseSelectSurvivesStreamingOutputMidDrag(t *testing.T) {
	m := e2eModel(t)
	top := m.headerH

	_, _ = m.Update(tea.MouseMsg{X: 0, Y: top, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.selecting {
		t.Fatal("press did not start a selection")
	}
	_, _ = m.Update(tea.MouseMsg{X: 5, Y: top + 2, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})

	// Output lands mid-drag and the view re-renders at the bottom.
	m.appendBlock(infoBlock{text: "late output"})
	m.dirty = true
	m.renderTranscript()

	_, cmd := m.Update(tea.MouseMsg{X: 5, Y: top + 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if cmd == nil {
		t.Fatal("streaming output mid-drag swallowed the copy")
	}
	if m.selecting {
		t.Error("release should end the selection")
	}
}

// The confirm sheet (and any other modal) renders below the transcript, so
// drag-select over the output must work with the sheet up — the real-world
// case of copying the agent's output while it waits for confirmation.
func TestMouseSelectWorksWhileDialogOpen(t *testing.T) {
	m := e2eModel(t)
	m.dlg = &dialog{kind: dlgConfirm, commands: []string{"ls"}, options: confirmOptions()}
	m.layout()
	top := m.headerH

	_, _ = m.Update(tea.MouseMsg{X: 0, Y: top, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.selecting {
		t.Fatal("press over the transcript must start a selection while the sheet is up")
	}
	_, _ = m.Update(tea.MouseMsg{X: 5, Y: top + 2, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	if !m.moved {
		t.Fatal("drag did not move the head while the sheet is up")
	}
	if m.dlg == nil {
		t.Fatal("the sheet should stay open during selection")
	}

	_, cmd := m.Update(tea.MouseMsg{X: 5, Y: top + 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if cmd == nil {
		t.Fatal("release over the transcript must copy while the sheet is up")
	}
	if m.dlg == nil {
		t.Fatal("the sheet should stay open after the copy")
	}
}

// Clicks over the modal sheet itself must not reach the transcript: they
// land below the viewport and fall outside the press guard.
func TestMouseClickOverSheetIgnored(t *testing.T) {
	m := e2eModel(t)
	m.dlg = &dialog{kind: dlgExit, options: exitOptions()}
	m.layout()
	sheetRow := m.headerH + m.vp.Height // first row of the sheet area

	_, cmd := m.Update(tea.MouseMsg{X: 10, Y: sheetRow, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.selecting {
		t.Fatal("a press over the sheet must not start a selection")
	}
	if cmd != nil {
		t.Fatal("a press over the sheet must not toggle a block")
	}
}

// A spinner tick between press and motion must not clear the selection.
func TestMouseSelectSurvivesSpinnerTickMidDrag(t *testing.T) {
	m := e2eModel(t)
	top := m.headerH

	_, _ = m.Update(tea.MouseMsg{X: 0, Y: top, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.selecting {
		t.Fatal("press did not start a selection")
	}
	_, _ = m.Update(spinner.TickMsg{})
	_, cmd := m.Update(tea.MouseMsg{X: 5, Y: top + 2, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	if cmd != nil && m.stick {
		t.Log("viewport emitted a follow command during a drag")
	}
	_, rel := m.Update(tea.MouseMsg{X: 5, Y: top + 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if rel == nil {
		t.Fatal("spinner tick mid-drag swallowed the copy")
	}
}
