package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// transcriptModel builds a model whose transcript overflows the viewport and
// starts pinned to the bottom, like a run that just printed its last block.
func transcriptModel(t *testing.T) *model {
	t.Helper()
	m := testModel()
	m.selAnchor, m.selHead = -1, -1
	m.w, m.h = 80, 24
	m.layout()
	for i := 0; i < 40; i++ {
		m.appendBlock(infoBlock{text: "line " + strings.Repeat("x", 8)})
	}
	m.dirty = true
	m.renderTranscript()
	m.layout()
	if m.vp.TotalLineCount() <= m.vp.Height {
		t.Fatalf("transcript does not overflow: %d lines, height %d", m.vp.TotalLineCount(), m.vp.Height)
	}
	if !m.vp.AtBottom() {
		t.Fatal("expected the transcript to start at the bottom")
	}
	return m
}

// The wheel must scroll the final output even while the finish sheet is up:
// the sheet has no content of its own to page.
func TestExitDialogWheelScrollsTranscript(t *testing.T) {
	m := transcriptModel(t)
	m.dlg = &dialog{kind: dlgExit, options: exitOptions()}
	m.layout()

	before := m.vp.YOffset
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.vp.YOffset >= before {
		t.Errorf("wheel up left the transcript at %d, want < %d", m.vp.YOffset, before)
	}
	if m.stick {
		t.Error("scrolling up should suspend auto-follow")
	}
}

// Page keys must scroll the final output while the finish sheet is up.
func TestExitDialogPageKeysScrollTranscript(t *testing.T) {
	m := transcriptModel(t)
	m.dlg = &dialog{kind: dlgExit, options: exitOptions()}
	m.layout()

	before := m.vp.YOffset
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.vp.YOffset >= before {
		t.Errorf("pgup left the transcript at %d, want < %d", m.vp.YOffset, before)
	}
	if m.stick {
		t.Error("scrolling up should suspend auto-follow")
	}
}

// Page keys scroll the transcript when no dialog owns the screen, and paging
// back to the bottom resumes auto-follow.
func TestTranscriptPageKeysScrollAndResumeFollow(t *testing.T) {
	m := transcriptModel(t)
	before := m.vp.YOffset
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.vp.YOffset >= before {
		t.Errorf("pgup left the transcript at %d, want < %d", m.vp.YOffset, before)
	}
	if m.stick {
		t.Error("scrolling up should suspend auto-follow")
	}

	for i := 0; i < 100 && !m.vp.AtBottom(); i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !m.vp.AtBottom() {
		t.Fatal("pgdown should reach the bottom of the transcript")
	}
	if !m.stick {
		t.Error("reaching the bottom should re-enable auto-follow")
	}
}

// A short approval has nothing to page, so the page keys scroll the final
// output instead of being swallowed by the modal.
func TestShortConfirmPageKeysScrollTranscript(t *testing.T) {
	m := transcriptModel(t)
	m.dlg = &dialog{kind: dlgConfirm, commands: []string{"ls"}, options: confirmOptions()}
	m.layout()
	if m.dlg.confirmScrollable(m.w) {
		t.Fatal("a short command should not overflow the confirm window")
	}

	before := m.vp.YOffset
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.vp.YOffset >= before {
		t.Errorf("pgup left the transcript at %d, want < %d", m.vp.YOffset, before)
	}
}

// A long approval keeps the page keys for its own command window; the
// transcript behind it must not move.
func TestLongConfirmPageKeysPageCommandsOnly(t *testing.T) {
	m := transcriptModel(t)
	m.dlg = &dialog{kind: dlgConfirm, commands: []string{longCommand()}, options: confirmOptions()}
	m.layout()
	if !m.dlg.confirmScrollable(m.w) {
		t.Fatal("a long command should overflow the confirm window")
	}

	transcriptBefore := m.vp.YOffset
	scrollBefore := m.dlg.scroll
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.dlg.scroll <= scrollBefore {
		t.Errorf("pgdown should advance the command window, got %d", m.dlg.scroll)
	}
	if m.vp.YOffset != transcriptBefore {
		t.Errorf("transcript moved to %d, want %d", m.vp.YOffset, transcriptBefore)
	}
}
