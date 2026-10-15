package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"stubbs/src/agent"
	"stubbs/src/types"
)

// ErrChatQuit is returned by NextInput once the chat program has stopped.
var ErrChatQuit = errors.New("chat: quit")

const chatIdlePlaceholder = "Ask anything…  (enter to send · ctrl+j for a newline · ctrl+c to stop)"

// ChatOptions configures the chat TUI.
type ChatOptions struct {
	Model string
	Theme string
}

// ChatApp is the Bubble Tea front-end for a chat session. It implements
// agent.ChatUI and exposes NextInput, which blocks until the user submits a
// message (or the program exits).
type ChatApp struct {
	prog      *tea.Program
	in        chan string
	interrupt func()
	closed    chan struct{}
	closeOnce sync.Once
}

type chatInputMsg struct{ text string }
type chatTurnDoneMsg struct{ err error }

// NewChat builds the chat TUI.
func NewChat(opts ChatOptions) *ChatApp {
	m := &chatModel{
		opts:  opts,
		st:    newStyles(loadTheme(opts.Theme)),
		in:    make(chan string, 1),
		stick: true, // follow the newest message until the user scrolls up
	}
	m.vp = viewport.New(80, 24)
	m.ta = newChatTextarea()
	m.sp = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	// Mouse cell motion lets the wheel scroll the transcript.
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	a := &ChatApp{prog: p, in: m.in, closed: make(chan struct{})}
	m.app = a
	return a
}

// SetInterrupt wires ctrl+c to the running turn.
func (a *ChatApp) SetInterrupt(f func()) { a.interrupt = f }

// Run runs the TUI until the user quits.
func (a *ChatApp) Run() error {
	_, err := a.prog.Run()
	a.closeOnce.Do(func() { close(a.closed) })
	return err
}

// Quit asks the TUI to stop; safe from any goroutine.
func (a *ChatApp) Quit() { a.prog.Quit() }

// NextInput blocks until the user submits a message. It returns ErrChatQuit
// when the program has stopped.
func (a *ChatApp) NextInput() (string, error) {
	select {
	case s := <-a.in:
		return s, nil
	case <-a.closed:
		return "", ErrChatQuit
	}
}

// TurnDone re-enables the composer after a turn finishes.
func (a *ChatApp) TurnDone(err error) { a.prog.Send(chatTurnDoneMsg{err: err}) }

// Status implements agent.ChatUI.
func (a *ChatApp) Status(text string) { a.prog.Send(statusMsg(text)) }

// Assistant implements agent.ChatUI.
func (a *ChatApp) Assistant(step int, cost float32, msg types.Message) {
	a.prog.Send(headerMsg{steps: step, cost: cost})
	calls := make([]string, len(msg.ToolCalls))
	for i, call := range msg.ToolCalls {
		calls[i] = agent.CommandOf(call)
	}
	a.prog.Send(blockMsg{chatAssistantBlock{content: msg.Content, calls: calls}})
}

// Observation implements agent.ChatUI.
func (a *ChatApp) Observation(call types.ToolCall, out types.ExecutionOutput) {
	a.prog.Send(blockMsg{toolBlock{
		name:     call.Function.Name,
		cmd:      agent.CommandOf(call),
		out:      out,
		expanded: true,
	}})
}

// --- bubbletea model ---

type chatModel struct {
	opts ChatOptions
	st   styles
	app  *ChatApp
	in   chan string

	w, h int
	vp   viewport.Model
	ta   textarea.Model
	sp   spinner.Model

	blocks []block
	dirty  bool
	stick  bool

	status    string
	statusErr bool
	working   bool
	steps     int
	cost      float32
}

func newChatTextarea() textarea.Model {
	ta := textarea.New()
	ta.Placeholder = chatIdlePlaceholder
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// enter submits in chat, so newline lives on ctrl+j (and ctrl+m still
	// inserts one via the default binding).
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "ctrl+m"))
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.Focus()
	return ta
}

func (m *chatModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.sp.Tick)
}

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		if m.stick {
			m.vp.GotoBottom()
		}
		return m, nil

	case blockMsg:
		m.appendBlock(msg.b)
		return m, nil

	case statusMsg:
		m.status = string(msg)
		m.statusErr = false
		return m, nil

	case headerMsg:
		m.steps, m.cost = msg.steps, msg.cost
		return m, nil

	case chatTurnDoneMsg:
		m.working = false
		m.statusErr = false
		switch {
		case errors.Is(msg.err, agent.ErrLimitsExceeded):
			m.statusErr = true
			m.status = "stopped: limit reached"
		case msg.err != nil:
			m.statusErr = true
			m.status = msg.err.Error()
			m.appendBlock(errorBlock{text: msg.err.Error()})
		default:
			m.status = ""
		}
		m.ta.Focus()
		m.layout()
		return m, textarea.Blink

	case tea.MouseMsg:
		// Only the wheel scrolls; clicks and drags are ignored so the
		// composer keeps focus.
		if msg.Action != tea.MouseActionPress ||
			(msg.Button != tea.MouseButtonWheelUp && msg.Button != tea.MouseButtonWheelDown) {
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.syncStick()
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.layout()
	return m, cmd
}

func (m *chatModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Transcript scrolling claims only keys the composer does not use, so
	// arrows and home/end keep moving the caret.
	if m.handleScrollKey(msg.String()) {
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c", "esc":
		if m.working {
			if m.app != nil && m.app.interrupt != nil {
				m.app.interrupt()
			}
			m.statusErr = false
			m.status = "stopping…"
			return m, nil
		}
		return m, tea.Quit
	case "enter":
		return m.submit()
	case "ctrl+o":
		for i, b := range m.blocks {
			if tb, ok := b.(toolBlock); ok {
				tb.expanded = !tb.expanded
				m.blocks[i] = tb
			}
		}
		m.dirty = true
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.layout()
	return m, cmd
}

// handleScrollKey scrolls the transcript for the given key and reports
// whether it consumed it. PgUp/PgDn page, shift+arrows move a line, and
// ctrl+home/ctrl+end jump to the first/last message.
func (m *chatModel) handleScrollKey(key string) bool {
	if m.vp.Height == 0 {
		return false
	}
	switch key {
	case "pgup":
		m.vp.PageUp()
	case "pgdown":
		m.vp.PageDown()
	case "shift+up":
		m.vp.ScrollUp(1)
	case "shift+down":
		m.vp.ScrollDown(1)
	case "ctrl+home":
		m.vp.GotoTop()
	case "ctrl+end":
		m.vp.GotoBottom()
	default:
		return false
	}
	m.syncStick()
	return true
}

// syncStick resumes auto-follow once the viewport is back at the bottom and
// pauses it the moment the user scrolls away.
func (m *chatModel) syncStick() { m.stick = m.vp.AtBottom() }

// scrollHint tells the user where they are when they have scrolled away from
// the latest message. Empty while following the bottom.
func (m *chatModel) scrollHint() string {
	if m.stick || m.vp.Height == 0 || m.vp.AtBottom() {
		return ""
	}
	return fmt.Sprintf("↑ %d%% · pgup/pgdn to scroll · ctrl+end for latest", int(m.vp.ScrollPercent()*100))
}

func (m *chatModel) submit() (tea.Model, tea.Cmd) {
	if m.working {
		m.statusErr = false
		m.status = "still working — ctrl+c to stop"
		return m, nil
	}
	text := strings.TrimSpace(m.ta.Value())
	if text == "" {
		return m, nil
	}
	m.ta.Reset()
	m.ta.SetHeight(1)
	m.layout()

	switch text {
	case "/clear":
		m.blocks = nil
		m.dirty = true
		m.stick = true
		m.status = ""
		m.statusErr = false
		return m, nil
	case "/help":
		m.appendBlock(infoBlock{text: chatHelp()})
		return m, nil
	}

	m.appendBlock(userBlock{text: text})
	select {
	case m.in <- text:
		m.working = true
		m.statusErr = false
		m.status = "thinking…"
	default:
		// The agent is between turns (or gone); nothing consumed the input.
		m.appendBlock(errorBlock{text: "could not send message — session is not accepting input"})
	}
	return m, textarea.Blink
}

func chatHelp() string {
	return strings.Join([]string{
		"chat keys:",
		"  enter        send",
		"  ctrl+j       newline",
		"  ctrl+c / esc stop the reply (again to quit)",
		"  ctrl+o       toggle tool output",
		"  pgup/pgdn    scroll the transcript (mouse wheel works too)",
		"  shift+up/down scroll one line",
		"  ctrl+home/end jump to the start/latest",
		"  /clear       clear the transcript",
		"  /help        show this help",
	}, "\n")
}

func (m *chatModel) appendBlock(b block) {
	if m.vp.AtBottom() || m.vp.Height == 0 {
		m.stick = true
	}
	m.blocks = append(m.blocks, b)
	m.dirty = true
}

func (m *chatModel) layout() {
	if m.w == 0 {
		return
	}
	inputH := textareaRows(m.ta.Value(), max(m.w-2, 1))
	if inputH < 1 {
		inputH = 1
	}
	maxInput := max(m.h/3, 1)
	if inputH > maxInput {
		inputH = maxInput
	}
	m.ta.SetWidth(max(m.w-2, 1))
	m.ta.SetHeight(inputH)
	m.vp.Width = max(m.w, 1)
	m.vp.Height = max(m.h-2-inputH-2, 1)
	if m.dirty {
		m.renderTranscript()
	}
}

func (m *chatModel) renderTranscript() {
	w := max(m.vp.Width, 1)
	var parts []string
	for _, b := range m.blocks {
		parts = append(parts, b.render(w, m.st))
	}
	m.vp.SetContent(strings.Join(parts, "\n"))
	m.dirty = false
	if m.stick {
		m.vp.GotoBottom()
	}
}

func (m *chatModel) header() string {
	left := m.st.title.Render("stubbs") + " " + m.st.badge("chat", m.st.pal.user)
	right := m.st.faint.Render(m.opts.Model) +
		m.st.dim.Render(fmt.Sprintf(" · step %d · $%.4f", m.steps, m.cost))
	if pad := m.w - plainWidth(left) - plainWidth(right); pad >= 1 {
		return left + strings.Repeat(" ", pad) + right
	}
	return wrapAt(m.w).Render(left)
}

func (m *chatModel) statusLine() string {
	var text string
	style := m.st.dim
	switch {
	case m.working:
		text = m.sp.View() + " " + m.status
		style = m.st.info
	case m.statusErr:
		text = "● " + m.status
		style = m.st.errStyle
	case m.status != "":
		text = "● " + m.status
		style = m.st.ok
	}
	if hint := m.scrollHint(); hint != "" {
		if text == "" {
			text, style = hint, m.st.faint
		} else {
			text += " · " + hint
		}
	}
	if text == "" {
		return ""
	}
	return style.Render(ansi.Truncate(text, max(m.w, 1), "…"))
}

func (m *chatModel) View() string {
	if m.w == 0 {
		return "loading…"
	}
	m.layout()
	box := m.st.box
	if !m.working {
		box = m.st.boxFocus
	}
	status := m.statusLine()
	var mid []string
	mid = append(mid, m.vp.View())
	if status != "" {
		mid = append(mid, status)
	}
	mid = append(mid, box.Render(m.ta.View()))
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), strings.Join(mid, "\n"))
}
