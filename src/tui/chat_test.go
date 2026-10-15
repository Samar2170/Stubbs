package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"stubbs/src/agent"
)

func testChatModel() *chatModel {
	m := &chatModel{st: newStyles(loadTheme("tokyo")), in: make(chan string, 1)}
	m.ta = newChatTextarea()
	return m
}

func TestChatSubmitDeliversInput(t *testing.T) {
	m := testChatModel()
	m.ta.SetValue("hello world")
	_, _ = m.submit()

	select {
	case got := <-m.in:
		if got != "hello world" {
			t.Fatalf("input = %q", got)
		}
	default:
		t.Fatal("expected input to be delivered")
	}
	if !m.working {
		t.Fatal("model should be working after submit")
	}
	if m.ta.Value() != "" {
		t.Fatalf("composer should be reset, got %q", m.ta.Value())
	}
}

func TestChatSubmitIgnoredWhileWorking(t *testing.T) {
	m := testChatModel()
	m.working = true
	m.ta.SetValue("ignored")
	_, _ = m.submit()
	select {
	case got := <-m.in:
		t.Fatalf("input should be ignored while working, got %q", got)
	default:
	}
}

func TestChatTurnDoneClearsWorking(t *testing.T) {
	m := testChatModel()
	m.working = true
	m.Update(chatTurnDoneMsg{})
	if m.working {
		t.Fatal("working should be false after turn done")
	}
}

func TestChatClearCommand(t *testing.T) {
	m := testChatModel()
	m.blocks = []block{userBlock{text: "hi"}}
	m.ta.SetValue("/clear")
	_, _ = m.submit()
	if len(m.blocks) != 0 {
		t.Fatalf("blocks = %d, want 0", len(m.blocks))
	}
	select {
	case got := <-m.in:
		t.Fatalf("/clear should not be sent to the agent, got %q", got)
	default:
	}
}

func TestChatCtrlCQuitsWhenIdle(t *testing.T) {
	m := testChatModel()
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c when idle should quit")
	}
}

func TestChatCtrlCInterruptsWhenWorking(t *testing.T) {
	m := testChatModel()
	called := false
	m.app = &ChatApp{interrupt: func() { called = true }}
	m.working = true
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatal("ctrl+c while working should interrupt, not quit")
	}
	if !called {
		t.Fatal("interrupt callback not called")
	}
}

func TestChatAssistantBlock(t *testing.T) {
	s := newStyles(loadTheme("tokyo"))
	b := chatAssistantBlock{content: "the answer", calls: []string{"web https://example.com"}}
	out := b.render(80, s)
	if !strings.Contains(out, "the answer") || !strings.Contains(out, "web https://example.com") {
		t.Fatalf("render missing content:\n%s", out)
	}
}

func TestChatAppImplementsChatUI(t *testing.T) {
	var _ agent.ChatUI = (*ChatApp)(nil)
}

// chatScrollModel builds a laid-out chat model whose transcript overflows the
// viewport, so scrolling can be exercised.
func chatScrollModel(t *testing.T, lines int) *chatModel {
	t.Helper()
	m := testChatModel()
	m.w, m.h = 40, 12
	rows := make([]string, lines)
	for i := range rows {
		rows[i] = fmt.Sprintf("line %02d", i)
	}
	m.blocks = []block{infoBlock{text: strings.Join(rows, "\n")}}
	m.dirty = true
	m.stick = true
	m.layout()
	m.vp.GotoBottom()
	if m.vp.AtTop() {
		t.Fatal("fixture transcript should overflow the viewport")
	}
	return m
}

func TestChatWheelScrollsTranscript(t *testing.T) {
	m := chatScrollModel(t, 60)
	bottom := m.vp.YOffset

	_, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	})
	if m.vp.YOffset >= bottom {
		t.Fatalf("wheel up should scroll back: offset %d -> %d", bottom, m.vp.YOffset)
	}
	if m.stick {
		t.Fatal("scrolling up should stop auto-follow")
	}
}

func TestChatScrollKeys(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
	}{
		{"pgup", tea.KeyMsg{Type: tea.KeyPgUp}},
		{"shift+up", tea.KeyMsg{Type: tea.KeyShiftUp}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := chatScrollModel(t, 60)
			bottom := m.vp.YOffset
			_, _ = m.handleKey(tc.key)
			if m.vp.YOffset >= bottom {
				t.Fatalf("%s should scroll back: offset %d -> %d", tc.name, bottom, m.vp.YOffset)
			}
		})
	}
}

func TestChatComposerKeepsArrowKeys(t *testing.T) {
	m := chatScrollModel(t, 60)
	offset := m.vp.YOffset
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if m.vp.YOffset != offset {
		t.Fatalf("plain up should stay with the composer, offset moved %d -> %d", offset, m.vp.YOffset)
	}
}

func TestChatScrollResumesFollowAtBottom(t *testing.T) {
	m := chatScrollModel(t, 60)
	m.vp.ScrollUp(5)
	m.syncStick()

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !m.stick {
		t.Fatal("ctrl+end should resume auto-follow")
	}
	if !m.vp.AtBottom() {
		t.Fatal("ctrl+end should jump to the latest message")
	}
}

func TestChatScrollHintWhenPagedUp(t *testing.T) {
	m := chatScrollModel(t, 60)
	if hint := m.scrollHint(); hint != "" {
		t.Fatalf("hint at bottom = %q, want empty", hint)
	}
	m.vp.ScrollUp(5)
	m.syncStick()
	if hint := m.scrollHint(); !strings.Contains(hint, "pgup") {
		t.Fatalf("hint while scrolled up = %q", hint)
	}
	if line := m.statusLine(); !strings.Contains(line, "ctrl+end") {
		t.Fatalf("status line should surface the hint, got %q", line)
	}
}

func TestChatAppendKeepsScrollPosition(t *testing.T) {
	m := chatScrollModel(t, 60)
	m.vp.ScrollUp(5)
	m.syncStick()
	offset := m.vp.YOffset

	m.appendBlock(userBlock{text: "a new message"})
	m.layout()

	if m.stick {
		t.Fatal("a new block must not re-enable auto-follow while scrolled up")
	}
	if m.vp.YOffset != offset {
		t.Fatalf("offset moved from %d to %d while scrolled up", offset, m.vp.YOffset)
	}
}

func TestChatClearResumesFollow(t *testing.T) {
	m := chatScrollModel(t, 60)
	m.vp.ScrollUp(5)
	m.syncStick()
	m.ta.SetValue("/clear")
	_, _ = m.submit()
	if !m.stick {
		t.Fatal("/clear should reset the transcript back to auto-follow")
	}
}
