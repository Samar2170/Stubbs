package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
)

// longCommand is a single command long enough to wrap to far more rows than
// the confirm viewport can ever show.
func longCommand() string {
	return strings.Repeat("some very long command segment ", 40)
}

func confirmModel(cmds ...string) *model {
	m := testModel()
	m.w, m.h = 80, 24
	m.dlg = &dialog{kind: dlgConfirm, commands: cmds, options: confirmOptions()}
	m.layout()
	return m
}

// A long approval must not grow the sheet past the row budget the layout
// assigned, otherwise it squeezes the transcript off screen.
func TestConfirmDialogClipsToRowBudget(t *testing.T) {
	m := confirmModel(longCommand())
	if !m.dlg.confirmScrollable(m.w) {
		t.Fatal("a very long command should overflow the window")
	}
	sheet := m.dlg.render(m.w, m.st)
	if got := lipglossHeight(sheet); got > m.dlg.rows {
		t.Errorf("confirm sheet is %d rows, budget is %d", got, m.dlg.rows)
	}
	if got := lipglossHeight(sheet); got > m.h-2 {
		t.Errorf("confirm sheet is %d rows, terminal is %d", got, m.h)
	}
	if m.dlg.region > maxConfirmCmdRows {
		t.Errorf("command region %d exceeds cap %d", m.dlg.region, maxConfirmCmdRows)
	}
}

// A single long command wraps to many rows; the window must page through
// them, revealing text the first window hid.
func TestConfirmDialogScrollRevealsTail(t *testing.T) {
	m := confirmModel(longCommand())
	first := m.dlg.render(m.w, m.st)
	if !strings.Contains(first, "█") {
		t.Fatal("a scrollable confirm dialog should draw a scrollbar thumb")
	}
	topRegion := m.dlg.region
	if topRegion < 1 {
		t.Fatalf("region = %d, want a visible window", topRegion)
	}
	if m.dlg.total <= m.dlg.region {
		t.Fatalf("total %d rows should exceed the region %d", m.dlg.total, m.dlg.region)
	}

	// Page all the way to the bottom.
	for i := 0; i < 500 && m.dlg.scroll+m.dlg.region < m.dlg.total; i++ {
		m.handleDialogKey("down")
	}
	if got := m.dlg.scroll + m.dlg.region; got != m.dlg.total {
		t.Fatalf("scrolled to row %d, want the end %d", got, m.dlg.total)
	}
	last := m.dlg.render(m.w, m.st)
	if last == first {
		t.Fatal("scrolling should change the rendered command window")
	}
	if want := max(topRegion*topRegion/m.dlg.total, 1); strings.Count(last, "█") != want {
		t.Errorf("thumb should span %d cells, got %d", want, strings.Count(last, "█"))
	}
	if got := lipglossHeight(last); got > m.dlg.rows {
		t.Errorf("scrolled sheet is %d rows, budget is %d", got, m.dlg.rows)
	}
}

// The arrow and page keys must page the command window while it overflows,
// and the scroll offset must clamp at both ends rather than blank the view.
func TestConfirmDialogScrollClamps(t *testing.T) {
	m := confirmModel(longCommand())
	for i := 0; i < 500; i++ {
		m.handleDialogKey("down")
		m.handleDialogKey("pgdown")
	}
	if want := m.dlg.total - m.dlg.region; m.dlg.scroll != want {
		t.Fatalf("scroll %d, want the last window start %d", m.dlg.scroll, want)
	}
	if m.dlg.scroll+m.dlg.region != m.dlg.total {
		t.Fatalf("window ends at %d, want the last row %d", m.dlg.scroll+m.dlg.region, m.dlg.total)
	}
	for i := 0; i < 500; i++ {
		m.handleDialogKey("up")
		m.handleDialogKey("pgup")
	}
	if m.dlg.scroll != 0 {
		t.Errorf("scroll should clamp at the top, got %d", m.dlg.scroll)
	}
}

// Short approvals keep the old behavior: no scrollbar, arrows move the
// selection, so "y" still approves.
func TestConfirmDialogShortCommandHasNoScrollbar(t *testing.T) {
	m := confirmModel("rm -rf /")
	view := m.dlg.render(m.w, m.st)
	if strings.Contains(view, "█") || strings.Contains(view, "scroll") {
		t.Error("a short command should not show a scrollbar or hint")
	}
	before := m.dlg.selected
	m.handleDialogKey("down")
	if m.dlg.selected != (before+1)%len(m.dlg.options) {
		t.Error("arrows should move the option selection when nothing overflows")
	}

	reply := make(chan inputResult, 1)
	m.dlg.reply = reply
	m.handleDialogKey("y")
	if res := <-reply; res.text != "" || res.interrupted || res.aborted {
		t.Errorf("y should approve, got %+v", res)
	}
}

// The wheel pages the command window instead of the transcript behind the
// modal, which must stay put (and keep following the bottom).
func TestConfirmDialogWheelScrollsCommands(t *testing.T) {
	m := confirmModel(longCommand())
	m.stick = true
	before := m.dlg.scroll
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.dlg.scroll <= before {
		t.Errorf("wheel down should advance the command window, got %d", m.dlg.scroll)
	}
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.dlg.scroll != before {
		t.Errorf("wheel up should restore the offset, got %d", m.dlg.scroll)
	}
	if !m.stick {
		t.Error("the transcript behind the modal must not lose auto-follow")
	}
}

// The rendered frame (with the modal open) must still fit the terminal: this
// is what guarantees the composer and transcript are not pushed off screen.
func TestConfirmDialogViewFitsTerminal(t *testing.T) {
	manyLines := strings.Split(strings.Repeat("line\n", 30), "\n")
	for _, size := range []struct{ w, h int }{{40, 12}, {60, 16}, {80, 24}, {100, 30}} {
		for _, cmds := range [][]string{
			{longCommand()},
			{longCommand(), "second command", "third command"},
			manyLines,
		} {
			m := testModel()
			m.w, m.h = size.w, size.h
			m.dlg = &dialog{kind: dlgConfirm, commands: cmds, options: confirmOptions()}
			m.layout()
			m.status = "agent is waiting for approval"
			m.layout()

			lines := strings.Split(m.View(), "\n")
			if len(lines) > size.h {
				t.Errorf("%dx%d: view has %d rows", size.w, size.h, len(lines))
			}
			for i, l := range lines {
				if ansi.StringWidth(l) > size.w {
					t.Errorf("%dx%d: row %d is %d wide", size.w, size.h, i, ansi.StringWidth(l))
				}
			}
		}
	}
}

// The scrollbar helper is pure, so pin its geometry directly: the thumb sits
// at the top before scrolling and reaches the bottom after.
func TestConfirmScrollbarGeometry(t *testing.T) {
	d := &dialog{kind: dlgConfirm, rows: 24, commands: []string{longCommand()}}
	d.render(80, styles{})
	n := d.region
	if n < 2 {
		t.Fatalf("region = %d, want a multi-row window", n)
	}
	top := d.scrollbar(0, n)
	if len(top) != n {
		t.Fatalf("scrollbar has %d rows, want %d", len(top), n)
	}
	if top[0] != " █" {
		t.Errorf("thumb should start at the top, got %q", top[0])
	}
	if top[n-1] != " │" {
		t.Errorf("track below the thumb should be empty, got %q", top[n-1])
	}

	bottom := d.scrollbar(d.total, n)
	if bottom[n-1] != " █" {
		t.Errorf("thumb should reach the bottom, got %q", bottom[n-1])
	}
	if bottom[0] != " │" {
		t.Errorf("track above the thumb should be empty, got %q", bottom[0])
	}

	// A dialog whose commands fit has no track to draw.
	short := &dialog{kind: dlgConfirm, rows: 24, commands: []string{"ls"}}
	short.render(80, styles{})
	if got := short.scrollbar(0, short.region); len(got) != short.region {
		t.Fatalf("empty scrollbar has %d rows, want %d", len(got), short.region)
	}
}

// An approval whose command is not yet rendered still needs a correct answer
// when the first key arrives (keyboard-only users have no wheel event to
// prime the geometry).
func TestConfirmScrollableWithoutPriorRender(t *testing.T) {
	m := testModel()
	m.w, m.h = 80, 24
	m.dlg = &dialog{kind: dlgConfirm, commands: []string{longCommand()}, options: confirmOptions()}
	if !m.dlg.confirmScrollable(m.w) {
		t.Fatal("an unrendered long command should still be scrollable")
	}
	if d := m.dlg; d.region <= 0 || d.total <= d.region {
		t.Fatalf("probing should record geometry, got total=%d region=%d", d.total, d.region)
	}
}

// A tight budget must not hide the command entirely: the sheet sheds its
// optional rows (blank separator, scroll footer, hint) before it starves the
// viewport, and one command row always survives. Twelve rows is the smallest
// terminal that can hold the header, status, one transcript row and the
// minimum sheet (title + four options + border + one command).
func TestConfirmDialogKeepsOneRowUnderTinyBudget(t *testing.T) {
	m := testModel()
	m.w, m.h = 80, 12
	m.dlg = &dialog{kind: dlgConfirm, commands: []string{longCommand()}, options: confirmOptions()}
	m.layout()
	if m.dlg.region < 1 {
		t.Fatalf("region = %d, want at least one command row", m.dlg.region)
	}
	if got := lipglossHeight(m.View()); got > m.h {
		t.Errorf("view is %d rows, terminal is %d", got, m.h)
	}
}

// lipglossHeight is the sheet height the layout budgets for.
func lipglossHeight(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(strings.TrimRight(s, "\n"), "\n"))
}
