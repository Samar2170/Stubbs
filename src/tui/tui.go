package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	"github.com/aymanbagabas/go-osc52/v2"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"stubbs/src/agent"
	"stubbs/src/types"
)

type inputKind int

const (
	inConfirm inputKind = iota
	inCommand
	inComment
	inExit
	inTask
	inLimits
	inReject
)

type inputResult struct {
	text        string
	interrupted bool // ctrl-c while this prompt was pending
	aborted     bool // ctrl-c at comment/limits/reject prompt: abort the run
	cancelled   bool // "q" at the limits prompt: end the run
}

type pendingInput struct {
	kind  inputKind
	title string
	reply chan inputResult
}

type blockMsg struct{ b block }
type statusMsg string
type copiedMsg int
type headerMsg struct {
	steps int
	cost  float32
}
type modeMsg agent.Mode
type inputReqMsg struct {
	kind     inputKind
	title    string
	commands []string
	reply    chan inputResult
}
type limitsReqMsg struct {
	curSteps, stepLimit int
	curCost, costLimit  float32
	reply               chan inputResult
}
type doneMsg struct {
	summary string
	ok      bool
}
type autoQuitMsg struct{}

// ModelChoice is a selectable entry from the provider's model catalog.
type ModelChoice struct {
	ID   string
	Name string
}

// SessionChoice is a selectable entry in the /sessions picker: a persisted
// session's id, the first message the user sent, and how recent it is.
type SessionChoice struct {
	ID           string
	FirstMessage string
	Updated      time.Time
	Messages     int
}

type modelMsg string
type modelsLoadedMsg struct {
	models []ModelChoice
	err    error
}
type modelSwitchedMsg struct {
	id  string
	err error
}
type repoMapMsg struct {
	err error
}
type sessionsLoadedMsg struct {
	sessions []SessionChoice
	err      error
}
type memoryListMsg struct {
	items []string
	err   error
}
type memoryOpMsg struct {
	status string
	err    error
}
type sessionOpenedMsg struct {
	id   string
	msgs []types.Message
	err  error
}

const idlePlaceholder = "Type here…  (/h for help)"

type Options struct {
	Model    string
	AutoQuit bool
	Theme    string
	Mode     agent.Mode
}

// MemoryHandlers wires the /remember, /forget, /memory and /map commands.
type MemoryHandlers struct {
	Remember func(text string) error
	Forget   func(query string) (int, error)
	List     func() ([]string, error)
	Map      func() error
}

// SessionHandlers wires the /sessions picker: List fetches the saved sessions
// and Open resumes one, returning the transcript to replay.
type SessionHandlers struct {
	List func() ([]SessionChoice, error)
	Open func(id string) ([]types.Message, error)
}

// App implements agent.UI on top of a full-screen Bubble Tea program.
// Ask* methods block on a reply channel that the Update loop resolves.
type App struct {
	prog      *tea.Program
	interrupt func()
	modelsFn  func() ([]ModelChoice, error)
	switchFn  func(id string) error
	memory    *MemoryHandlers
	sessions  *SessionHandlers
	autoQuit  bool
	closed    chan struct{}
	closeOnce sync.Once
}

func New(opts Options) *App {
	m := &model{
		opts:        opts,
		st:          newStyles(loadTheme(opts.Theme)),
		mode:        opts.Mode,
		showTools:   true,
		layoutDirty: true,
		selAnchor:   -1,
		selHead:     -1,
		selStyledLo: -1,
		selStyledHi: -1,
	}
	m.vp = viewport.New(80, 24)
	m.ta = newTextarea()
	m.sp = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	a := &App{prog: p, autoQuit: opts.AutoQuit, closed: make(chan struct{})}
	m.app = a
	return a
}

// SetInterrupt wires Ctrl-C to the agent (call after the agent is built).
func (a *App) SetInterrupt(f func()) { a.interrupt = f }

// SetModelHandlers wires the model catalog fetch and the switch callbacks used
// by the /models picker.
func (a *App) SetModelHandlers(fetch func() ([]ModelChoice, error), switchTo func(string) error) {
	a.modelsFn = fetch
	a.switchFn = switchTo
}

// SetMemoryHandlers wires the callbacks used by the /remember, /forget and
// /memory commands.
func (a *App) SetMemoryHandlers(h MemoryHandlers) { a.memory = &h }

// SetSessionHandlers wires the callbacks used by the /sessions picker.
func (a *App) SetSessionHandlers(h SessionHandlers) { a.sessions = &h }

// Run runs the TUI until the user quits. When it returns, any prompt that is
// still waiting for an answer is released so the agent goroutine can stop.
func (a *App) Run() error {
	_, err := a.prog.Run()
	a.closeOnce.Do(func() { close(a.closed) })
	return err
}

// Quit asks the TUI to stop; safe to call from any goroutine.
func (a *App) Quit() { a.prog.Quit() }

// AwaitTask blocks until the user submits the initial task.
func (a *App) AwaitTask() (string, error) {
	return a.ask(inTask, "What do you want to do?")
}

// ShowUser displays a user message in the transcript (e.g. a task passed on
// the command line). Messages typed in the TUI are echoed automatically.
func (a *App) ShowUser(text string) {
	a.prog.Send(blockMsg{userBlock{text: text}})
}

// Finish reports the run outcome and lets the user quit with ctrl-c.
func (a *App) Finish(submission string, err error) {
	summary, ok := finishSummary(err)
	a.prog.Send(doneMsg{summary: summary, ok: ok})
	if a.autoQuit {
		a.prog.Send(autoQuitMsg{})
	}
}

func finishSummary(err error) (string, bool) {
	switch {
	case err == nil:
		return "Run complete.", true
	case errors.Is(err, agent.ErrAborted):
		return "Run aborted by user.", false
	case errors.Is(err, agent.ErrInterrupted):
		return "Run interrupted.", false
	case errors.Is(err, agent.ErrLimitsExceeded):
		return "Run ended: limits exceeded.", false
	case errors.Is(err, context.Canceled):
		return "Run canceled.", false
	default:
		return "Run failed: " + err.Error(), false
	}
}

func (a *App) ask(kind inputKind, title string) (string, error) {
	reply := make(chan inputResult, 1)
	a.prog.Send(inputReqMsg{kind: kind, title: title, reply: reply})
	var res inputResult
	select {
	case res = <-reply:
	case <-a.closed:
		return "", agent.ErrInterrupted
	}
	switch {
	case res.aborted:
		return "", agent.ErrAborted
	case res.interrupted:
		return "", agent.ErrInterrupted
	}
	return res.text, nil
}

func (a *App) Info(format string, args ...any) {
	a.prog.Send(blockMsg{infoBlock{text: fmt.Sprintf(format, args...)}})
}

func (a *App) Assistant(step int, cost float32, msg types.Message) {
	a.prog.Send(headerMsg{steps: step, cost: cost})
	calls := make([]string, len(msg.ToolCalls))
	for i, call := range msg.ToolCalls {
		calls[i] = agent.CommandOf(call)
	}
	a.prog.Send(blockMsg{assistantBlock{
		step:    step,
		cost:    cost,
		content: msg.Content,
		calls:   calls,
	}})
}

func (a *App) Observation(call types.ToolCall, out types.ExecutionOutput) {
	a.prog.Send(blockMsg{toolBlock{
		name:     call.Function.Name,
		cmd:      agent.CommandOf(call),
		out:      out,
		expanded: true,
	}})
}

func (a *App) Status(text string) { a.prog.Send(statusMsg(text)) }

func (a *App) ModeChanged(m agent.Mode) { a.prog.Send(modeMsg(m)) }

func (a *App) ModelChanged(model string) { a.prog.Send(modelMsg(model)) }

func (a *App) AskConfirm(commands []string) (string, error) {
	reply := make(chan inputResult, 1)
	a.prog.Send(inputReqMsg{kind: inConfirm, commands: commands, reply: reply})
	return a.await(reply)
}

func (a *App) AskExit() (string, error) {
	reply := make(chan inputResult, 1)
	a.prog.Send(inputReqMsg{kind: inExit, reply: reply})
	return a.await(reply)
}

func (a *App) await(reply chan inputResult) (string, error) {
	var res inputResult
	select {
	case res = <-reply:
	case <-a.closed:
		return "", agent.ErrInterrupted
	}
	switch {
	case res.aborted:
		return "", agent.ErrAborted
	case res.interrupted:
		return "", agent.ErrInterrupted
	}
	return res.text, nil
}

func (a *App) AskCommand() (string, error) { return a.ask(inCommand, "Your command") }

func (a *App) AskComment() (string, error) {
	return a.ask(inComment, "Comment (ctrl-c again to abort)")
}

func (a *App) AskNewLimits(curSteps, stepLimit int, curCost, costLimit float32) (int, float32, bool, error) {
	reply := make(chan inputResult, 1)
	a.prog.Send(limitsReqMsg{
		curSteps: curSteps, stepLimit: stepLimit,
		curCost: curCost, costLimit: costLimit,
		reply: reply,
	})
	var res inputResult
	select {
	case res = <-reply:
	case <-a.closed:
		return 0, 0, false, agent.ErrAborted
	}
	if res.aborted || res.interrupted {
		return 0, 0, false, agent.ErrAborted
	}
	if res.cancelled {
		return 0, 0, false, nil
	}
	fields := strings.Fields(res.text)
	if len(fields) != 2 {
		return 0, 0, false, nil
	}
	steps, err1 := strconv.Atoi(fields[0])
	cost, err2 := strconv.ParseFloat(fields[1], 32)
	if err1 != nil || err2 != nil || steps <= 0 || cost < 0 {
		return 0, 0, false, nil
	}
	return steps, float32(cost), true, nil
}

// --- bubbletea model ---

// tLine is one rendered transcript line: the styled version for display and
// its plain text for selection/copy.
type tLine struct {
	plain string
	ansi  string
}

type model struct {
	opts        Options
	app         *App
	st          styles
	w, h        int
	inputH      int
	headerH     int // rows the header occupies
	statusRows  int // rows budgeted to the status line
	pendingRows int // rows budgeted to the pending prompt
	blocks      []block
	rows        []int // cumulative transcript line count per block
	tLines      []tLine
	viewLines   []string // styled viewport lines, kept in step with tLines
	selStyledLo int      // last selection range styled into viewLines (-1 = none)
	selStyledHi int
	selAnchor   int // selection anchor line (-1 = no selection)
	selHead     int // selection head line
	selecting   bool
	stick       bool // follow the bottom of the transcript
	showTools   bool // global expand/collapse for tool output (start expanded)
	dirty       bool // transcript needs a full re-render (width change/toggle)
	layoutDirty bool // layout() needs to re-run before the next View
	headerView  string
	statusView  string
	vp          viewport.Model
	ta          textarea.Model
	sp          spinner.Model
	spinning    bool
	status      string
	statusErr   bool
	statusSince time.Time
	working     bool
	doneOk      bool
	mode        agent.Mode
	steps       int
	cost        float32
	pending     *pendingInput
	menu        commandMenu
	dlg         *dialog
	expanded    bool
	done        bool
}

func newTextarea() textarea.Model {
	ta := textarea.New()
	ta.Placeholder = idlePlaceholder
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// bubbles paints the cursor line with a black background by default, which
	// shows through as a dark band over the text being typed. Drop it.
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.Focus()
	return ta
}

func (m *model) Init() tea.Cmd {
	// The spinner only ticks while the agent is busy (see spinnerCmd), so an
	// idle TUI never repaints just to animate it.
	return textarea.Blink
}

// spinnerCmd starts the spinner tick if work is in flight and it is not already
// running. Callers that turn m.working on should return it alongside their own
// command so the animation starts immediately.
func (m *model) spinnerCmd() tea.Cmd {
	if !m.busy() || m.spinning {
		return nil
	}
	m.spinning = true
	return m.sp.Tick
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		return m, nil

	case blockMsg:
		m.appendBlock(msg.b)
		return m, nil

	case statusMsg:
		m.status = string(msg)
		m.statusErr = false
		m.working = true
		m.statusSince = time.Now()
		m.layoutDirty = true
		return m, m.spinnerCmd()

	case headerMsg:
		m.steps, m.cost = msg.steps, msg.cost
		m.layoutDirty = true
		return m, nil

	case modeMsg:
		m.mode = agent.Mode(msg)
		m.layoutDirty = true
		return m, nil

	case modelMsg:
		m.opts.Model = string(msg)
		m.layoutDirty = true
		return m, nil

	case modelsLoadedMsg:
		m.working = false
		m.layoutDirty = true
		if msg.err != nil {
			m.statusErr = true
			m.status = "could not load models: " + msg.err.Error()
			return m, nil
		}
		if len(msg.models) == 0 {
			m.statusErr = true
			m.status = "no models available"
			return m, nil
		}
		m.status = ""
		m.statusErr = false
		m.dlg = newModelsDialog(msg.models)
		m.layout()
		return m, nil

	case modelSwitchedMsg:
		m.working = false
		m.layoutDirty = true
		if msg.err != nil {
			m.statusErr = true
			m.status = "switch failed: " + msg.err.Error()
			return m, nil
		}
		m.statusErr = false
		m.status = "model: " + msg.id
		return m, nil

	case repoMapMsg:
		m.working = false
		m.layoutDirty = true
		if msg.err != nil {
			m.statusErr = true
			m.status = "repo map failed: " + msg.err.Error()
			return m, nil
		}
		m.statusErr = false
		m.status = "repository map updated"
		return m, nil

	case sessionsLoadedMsg:
		m.working = false
		m.layoutDirty = true
		if msg.err != nil {
			m.statusErr = true
			m.status = "could not load sessions: " + msg.err.Error()
			return m, nil
		}
		if len(msg.sessions) == 0 {
			m.statusErr = false
			m.status = "no previous sessions"
			return m, nil
		}
		m.status = ""
		m.statusErr = false
		m.dlg = newSessionsDialog(msg.sessions)
		m.layout()
		return m, nil

	case sessionOpenedMsg:
		m.working = false
		m.layoutDirty = true
		if msg.err != nil {
			m.statusErr = true
			m.status = "could not open session: " + msg.err.Error()
			return m, nil
		}
		m.statusErr = false
		m.status = "resumed session " + msg.id
		m.replaySession(msg.msgs)
		return m, nil

	case memoryListMsg:
		m.working = false
		m.layoutDirty = true
		if msg.err != nil {
			m.statusErr = true
			m.status = "memory: " + msg.err.Error()
			return m, nil
		}
		m.statusErr = false
		if len(msg.items) == 0 {
			m.status = "memory is empty"
			return m, nil
		}
		// The listing can be long, so it goes into the scrollable transcript
		// where each entry wraps; the status stays short so it does not crowd
		// out the composer.
		m.status = fmt.Sprintf("%d memories", len(msg.items))
		m.appendBlock(infoBlock{text: fmt.Sprintf("%d memories:\n%s", len(msg.items), strings.Join(msg.items, "\n"))})
		return m, nil

	case memoryOpMsg:
		m.working = false
		m.layoutDirty = true
		if msg.err != nil {
			m.statusErr = true
			m.status = "memory: " + msg.err.Error()
			return m, nil
		}
		m.statusErr = false
		m.status = msg.status
		return m, nil

	case inputReqMsg:
		m.working = false
		m.menu.close()
		if msg.kind == inConfirm || msg.kind == inExit {
			d := &dialog{reply: msg.reply}
			if msg.kind == inConfirm {
				d.kind, d.commands, d.options = dlgConfirm, msg.commands, confirmOptions()
			} else {
				d.kind, d.options = dlgExit, exitOptions()
			}
			m.dlg = d
			m.layout()
			return m, nil
		}
		m.pending = &pendingInput{kind: msg.kind, title: msg.title, reply: msg.reply}
		m.ta.Placeholder = m.placeholderFor(msg.kind)
		m.ta.Reset()
		m.ta.Focus()
		m.layoutDirty = true
		return m, textarea.Blink

	case limitsReqMsg:
		m.working = false
		m.pending = &pendingInput{kind: inLimits, title: "Raise limits", reply: msg.reply}
		m.menu.close()
		m.ta.Placeholder = fmt.Sprintf("new limits, e.g. '%d %.2f'  ·  q ends the run", msg.stepLimit, msg.costLimit)
		m.ta.Reset()
		m.ta.Focus()
		m.layoutDirty = true
		return m, textarea.Blink

	case doneMsg:
		m.done = true
		m.working = false
		m.pending = nil
		m.dlg = nil
		m.status = msg.summary + " — ctrl+c or esc to quit"
		m.doneOk = msg.ok
		m.statusErr = !msg.ok
		m.menu.close()
		m.ta.Reset()
		m.ta.Blur()
		m.layout()
		if msg.ok {
			m.appendBlock(okBlock{text: msg.summary})
		} else {
			m.appendBlock(errorBlock{text: msg.summary})
		}
		return m, nil

	case autoQuitMsg:
		return m, tea.Quit

	case spinner.TickMsg:
		if !m.busy() {
			m.spinning = false
			m.statusSince = time.Time{}
			return m, nil
		}
		if m.statusSince.IsZero() {
			m.statusSince = msg.Time
		}
		m.layoutDirty = true // the elapsed-time suffix changes the status text
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd

	case copiedMsg:
		m.working = false
		m.layoutDirty = true
		m.status = fmt.Sprintf("copied %d line%s to clipboard", int(msg), pluralLines(int(msg)))
		return m, nil

	case tea.MouseMsg:
		// A modal owns the mouse: the wheel pages the confirm dialog's
		// command list rather than scrolling the transcript behind it.
		if m.dlg != nil {
			if d, ok := m.wheelConfirm(msg); ok {
				return m, d
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		if cp := m.handleMouse(msg); cp != nil {
			cmd = tea.Batch(cmd, cp)
		}
		if !m.vp.AtBottom() {
			m.stick = false // the user scrolled up; stop auto-follow
		}
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmds []tea.Cmd
	if m.dlg == nil {
		cmds = append(cmds, m.updateTA(msg))
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	cmds = append(cmds, cmd)
	if !m.vp.AtBottom() {
		m.stick = false // the user scrolled up; stop auto-follow
	}
	return m, tea.Batch(cmds...)
}

// wheelConfirm maps a wheel event over the confirm dialog to a viewport page.
// The ok result is false when the modal is not a scrollable confirm dialog,
// so the caller can swallow the event without touching the transcript.
func (m *model) wheelConfirm(msg tea.MouseMsg) (tea.Cmd, bool) {
	if msg.Action != tea.MouseActionPress || m.dlg == nil || m.dlg.kind != dlgConfirm {
		return nil, false
	}
	var delta int
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		delta = -1
	case tea.MouseButtonWheelDown:
		delta = 1
	default:
		return nil, false
	}
	cmd, _ := m.scrollConfirm(delta)
	return cmd, true
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.selAnchor >= 0 {
		m.clearSelection()
	}
	if m.dlg != nil {
		return m.handleDialogKey(msg.String())
	}
	if m.menu.open {
		switch msg.String() {
		case "up", "ctrl+p":
			m.menu.move(-1)
			return m, nil
		case "down", "ctrl+n":
			m.menu.move(1)
			return m, nil
		case "tab":
			return m, m.acceptCommand(false)
		case "enter":
			return m, m.acceptCommand(true)
		case "esc":
			m.menu.close()
			m.layout()
			return m, nil
		}
	}
	switch msg.String() {
	case "ctrl+c":
		if m.done {
			return m, tea.Quit
		}
		return m.interruptKey()

	case "esc":
		if m.done {
			return m, tea.Quit
		}
		return m.interruptKey()

	case "ctrl+o":
		m.showTools = !m.showTools
		for i, b := range m.blocks {
			if tb, ok := b.(toolBlock); ok {
				tb.expanded = m.showTools
				m.blocks[i] = tb
			}
		}
		m.dirty = true
		return m, nil

	case "ctrl+e":
		m.expanded = !m.expanded
		m.layout()
		return m, nil

	case "enter":
		if m.pending != nil {
			return m, m.submitPending()
		}
		if !m.done {
			m.status = "agent is busy — ctrl-c interrupts"
			m.layoutDirty = true
		}
		return m, nil
	}

	return m, m.updateTA(msg)
}

// updateTA forwards msg to the composer and re-lays out the view when the
// content grows or shrinks, so the box keeps the cursor visible. It also keeps
// the slash-command menu in step with what was typed.
func (m *model) updateTA(msg tea.Msg) tea.Cmd {
	before := m.desiredInputH()
	menuBefore := m.menuHeight()
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.syncMenu()
	if m.desiredInputH() != before || m.menuHeight() != menuBefore {
		m.layout()
	}
	return cmd
}

func (m *model) handleDialogKey(key string) (tea.Model, tea.Cmd) {
	d := m.dlg
	if d.kind == dlgModels || d.kind == dlgSessions {
		switch key {
		case "ctrl+c":
			if m.done {
				return m, tea.Quit
			}
			m.dlg = nil
			m.layout()
			return m, nil
		case "esc":
			m.dlg = nil
			m.layout()
			return m, nil
		case "up", "shift+tab":
			if n := len(d.options); n > 0 {
				d.selected = (d.selected + n - 1) % n
			}
			return m, nil
		case "down", "tab":
			if n := len(d.options); n > 0 {
				d.selected = (d.selected + 1) % n
			}
			return m, nil
		case "enter":
			return m, m.pickDialogOption()
		case "backspace":
			if d.filter != "" {
				d.filter = d.filter[:len(d.filter)-1]
				d.applyFilter()
				m.layoutDirty = true
			}
			return m, nil
		default:
			if r := []rune(key); len(r) == 1 && r[0] >= 0x20 {
				d.filter += key
				d.applyFilter()
				m.layoutDirty = true
			}
			return m, nil
		}
	}
	switch key {
	case "ctrl+c":
		if m.done {
			return m, tea.Quit
		}
		if d.reply != nil {
			m.resolveReply(d.reply, inputResult{interrupted: true})
		} else {
			m.dlg = nil
			m.layoutDirty = true
		}
		return m, nil

	case "esc":
		switch d.kind {
		case dlgHelp:
			m.dlg = nil
			m.layoutDirty = true
		case dlgConfirm:
			m.swapComposer(inReject, "Rejecting — what went wrong?", "what should the agent do instead? · ctrl-c=abort")
		default: // exit: esc finishes
			m.resolveReply(d.reply, inputResult{})
		}
		return m, nil

	case "up", "k":
		// In the confirm dialog a long command owns the arrow keys: they
		// page the command viewport instead of moving the option list, so
		// every line of a huge command stays reachable. The options are two
		// rows away, so Ctrl+P/Ctrl+N still cycle them.
		if n, handled := m.scrollConfirm(-1); handled {
			return m, n
		}
		d.selected = (d.selected + len(d.options) - 1) % len(d.options)
		return m, nil

	case "down", "j", "tab":
		if n, handled := m.scrollConfirm(1); handled {
			return m, n
		}
		d.selected = (d.selected + 1) % len(d.options)
		return m, nil

	case "ctrl+p":
		d.selected = (d.selected + len(d.options) - 1) % len(d.options)
		return m, nil

	case "ctrl+n":
		d.selected = (d.selected + 1) % len(d.options)
		return m, nil

	case "pgup", "ctrl+b":
		if n, handled := m.scrollConfirm(-m.dlg.confirmPage()); handled {
			return m, n
		}
		return m, nil

	case "pgdown", "ctrl+f":
		if n, handled := m.scrollConfirm(m.dlg.confirmPage()); handled {
			return m, n
		}
		return m, nil

	case "home":
		if d.kind == dlgConfirm && d.confirmScrollable(m.w) {
			d.scroll, d.selected = 0, 0
			return m, nil
		}
		return m, nil

	case "enter":
		if d.kind == dlgHelp {
			m.dlg = nil
			m.layoutDirty = true
			return m, nil
		}
		return m, m.pickDialogOption()

	case "y":
		if d.kind != dlgHelp {
			d.selected = 0
			return m, m.pickDialogOption()
		}
		return m, nil

	case "n":
		if d.kind == dlgExit {
			d.selected = 1
			return m, m.pickDialogOption()
		}
		return m, nil

	case "h":
		if d.kind == dlgExit {
			d.selected = 2
			return m, m.pickDialogOption()
		}
		return m, nil

	case "q":
		if d.kind == dlgHelp {
			m.dlg = nil
			m.layoutDirty = true
		}
		return m, nil
	}
	return m, nil // the modal swallows everything else
}

// scrollConfirm pages the confirm dialog's command viewport by delta lines.
// It returns a no-op command and true when the key was consumed, so the key
// handler knows not to also move the option selection. Scrolling is only
// claimed once the commands actually overflow the window; a short approval
// keeps the plain option-list navigation.
func (m *model) scrollConfirm(delta int) (tea.Cmd, bool) {
	d := m.dlg
	if d == nil || d.kind != dlgConfirm || !d.confirmScrollable(m.w) {
		return nil, false
	}
	d.pageCommands(delta, m.w, m.st)
	return nil, true
}

func (m *model) pickDialogOption() tea.Cmd {
	d := m.dlg
	if d == nil {
		return nil
	}
	if d.selected >= len(d.options) {
		d.selected = 0
	}
	if len(d.options) == 0 {
		return nil
	}
	o := d.options[d.selected]
	switch {
	case d.kind == dlgModels:
		return m.switchModel(o.text)
	case d.kind == dlgSessions:
		return m.openSession(o.text)
	}
	switch o.action {
	case dlgReject:
		m.swapComposer(inReject, "Rejecting — what went wrong?", m.placeholderFor(inReject))
	case dlgNewTask:
		m.swapComposer(inTask, "New task", m.placeholderFor(inTask))
	default:
		m.resolveReply(d.reply, inputResult{text: o.text})
	}
	return nil
}

// switchModel asks the app to switch (and persist) the active model. It runs
// off the Update loop so the blocking call and the resulting UI notification
// cannot deadlock the tea program.
func (m *model) switchModel(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	m.dlg = nil
	m.working = true
	m.statusErr = false
	m.status = "switching to " + id + "…"
	m.layout()
	app := m.app
	doSwitch := func() tea.Msg {
		if app == nil || app.switchFn == nil {
			return modelSwitchedMsg{id: id, err: errors.New("model switching is not wired up")}
		}
		if err := app.switchFn(id); err != nil {
			return modelSwitchedMsg{id: id, err: err}
		}
		return modelSwitchedMsg{id: id}
	}
	return tea.Batch(m.spinnerCmd(), doSwitch)
}

// loadModels fetches the provider's model catalog off the Update loop.
func (m *model) loadModels() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		if app == nil || app.modelsFn == nil {
			return modelsLoadedMsg{err: errors.New("model list is not wired up")}
		}
		models, err := app.modelsFn()
		return modelsLoadedMsg{models: models, err: err}
	}
}

// loadSessions fetches the saved session list off the Update loop.
func (m *model) loadSessions() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		if app == nil || app.sessions == nil || app.sessions.List == nil {
			return sessionsLoadedMsg{err: errors.New("session list is not wired up")}
		}
		sessions, err := app.sessions.List()
		return sessionsLoadedMsg{sessions: sessions, err: err}
	}
}

// openSession resumes a saved session off the Update loop and returns its
// transcript for replay.
func (m *model) openSession(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	m.dlg = nil
	m.working = true
	m.statusErr = false
	m.status = "opening session…"
	m.layout()
	app := m.app
	doOpen := func() tea.Msg {
		if app == nil || app.sessions == nil || app.sessions.Open == nil {
			return sessionOpenedMsg{id: id, err: errors.New("session resume is not wired up")}
		}
		msgs, err := app.sessions.Open(id)
		return sessionOpenedMsg{id: id, msgs: msgs, err: err}
	}
	return tea.Batch(m.spinnerCmd(), doOpen)
}

// replaySession clears the transcript and redraws a resumed session, then
// keeps the active prompt (the initial task prompt) so the user can type a
// follow-up task that continues the resumed conversation.
func (m *model) replaySession(msgs []types.Message) {
	m.blocks = nil
	m.rows = nil
	m.tLines = nil
	m.viewLines = nil
	m.selStyledLo, m.selStyledHi = -1, -1
	m.dirty = true
	for _, msg := range msgs {
		switch msg.Role {
		case types.RoleUser:
			if strings.TrimSpace(msg.Content) != "" {
				m.appendBlock(userBlock{text: msg.Content})
			}
		case types.RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				m.appendBlock(assistantBlock{content: msg.Content})
			}
		case types.RoleTool:
			m.appendBlock(toolBlock{
				name:     msg.Name,
				out:      types.ExecutionOutput{Output: msg.Content},
				expanded: false,
			})
		}
	}
	m.appendBlock(infoBlock{text: "resumed session — type a follow-up task to continue"})
	m.working = false
	m.done = false
	m.stick = true
	// Deliberately keep m.pending: opening a session happens before a task is
	// submitted, and the same prompt now collects the follow-up task.
	m.menu.close()
	m.dlg = nil
	m.ta.Reset()
	m.ta.Focus()
	m.layout()
}

func (m *model) generateRepoMap() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		if app == nil || app.memory == nil || app.memory.Map == nil {
			return repoMapMsg{err: errors.New("repo map is not wired up")}
		}
		return repoMapMsg{err: app.memory.Map()}
	}
}

func (m *model) memoryCommand(text string) (tea.Cmd, bool) {
	if m.app == nil || m.app.memory == nil {
		return nil, false
	}
	h := m.app.memory
	switch {
	case text == "/map":
		if h.Map == nil {
			m.statusErr = true
			m.status = "repo map is not wired up"
			m.layoutDirty = true
			return nil, true
		}
		m.statusErr = false
		m.working = true
		m.status = "generating repository map…"
		m.layoutDirty = true
		return tea.Batch(m.spinnerCmd(), m.generateRepoMap()), true
	case text == "/memory":
		if h.List == nil {
			m.statusErr = true
			m.status = "memory is not wired up"
			m.layoutDirty = true
			return nil, true
		}
		m.statusErr = false
		m.working = true
		m.status = "loading memories…"
		m.layoutDirty = true
		return tea.Batch(m.spinnerCmd(), m.listMemoryCmd()), true
	case strings.HasPrefix(text, "/remember"):
		body := strings.TrimSpace(strings.TrimPrefix(text, "/remember"))
		if body == "" {
			m.statusErr = true
			m.status = "usage: /remember <text>"
			m.layoutDirty = true
			return nil, true
		}
		if h.Remember == nil {
			m.statusErr = true
			m.status = "memory is not wired up"
			m.layoutDirty = true
			return nil, true
		}
		m.statusErr = false
		m.working = true
		m.status = "remembering…"
		m.layoutDirty = true
		return tea.Batch(m.spinnerCmd(), m.rememberCmd(body)), true
	case strings.HasPrefix(text, "/forget"):
		query := strings.TrimSpace(strings.TrimPrefix(text, "/forget"))
		if query == "" {
			m.statusErr = true
			m.status = "usage: /forget <id or query>"
			m.layoutDirty = true
			return nil, true
		}
		if h.Forget == nil {
			m.statusErr = true
			m.status = "memory is not wired up"
			m.layoutDirty = true
			return nil, true
		}
		m.statusErr = false
		m.working = true
		m.status = "forgetting…"
		m.layoutDirty = true
		return tea.Batch(m.spinnerCmd(), m.forgetCmd(query)), true
	}
	return nil, false
}

// listMemoryCmd loads the memory listing off the Update loop.
func (m *model) listMemoryCmd() tea.Cmd {
	app := m.app
	return func() tea.Msg {
		if app == nil || app.memory == nil || app.memory.List == nil {
			return memoryListMsg{err: errors.New("memory is not wired up")}
		}
		items, err := app.memory.List()
		return memoryListMsg{items: items, err: err}
	}
}

// rememberCmd persists a memory entry off the Update loop.
func (m *model) rememberCmd(text string) tea.Cmd {
	app := m.app
	return func() tea.Msg {
		if app == nil || app.memory == nil || app.memory.Remember == nil {
			return memoryOpMsg{err: errors.New("memory is not wired up")}
		}
		return memoryOpMsg{status: "remembered", err: app.memory.Remember(text)}
	}
}

// forgetCmd deletes matching memories off the Update loop.
func (m *model) forgetCmd(query string) tea.Cmd {
	app := m.app
	return func() tea.Msg {
		if app == nil || app.memory == nil || app.memory.Forget == nil {
			return memoryOpMsg{err: errors.New("memory is not wired up")}
		}
		n, err := app.memory.Forget(query)
		return memoryOpMsg{status: fmt.Sprintf("forgot %d memory entries", n), err: err}
	}
}

// swapComposer turns the active dialog into a composer prompt that answers
// the same pending request.
func (m *model) swapComposer(kind inputKind, title string, placeholder string) {
	m.pending = &pendingInput{kind: kind, title: title, reply: m.dlg.reply}
	m.dlg = nil
	m.menu.close()
	m.ta.Reset()
	m.ta.Placeholder = placeholder
	m.ta.Focus()
	m.layout()
}

func (m *model) interruptKey() (tea.Model, tea.Cmd) {
	if m.pending != nil {
		res := inputResult{interrupted: true}
		switch m.pending.kind {
		case inComment, inLimits, inReject:
			res = inputResult{aborted: true}
		}
		m.resolve(res)
		return m, nil
	}
	if m.app != nil && m.app.interrupt != nil {
		m.app.interrupt()
		m.status = "interrupting — tell the agent what happened…"
		m.working = true
		m.layoutDirty = true
		return m, m.spinnerCmd()
	}
	return m, nil
}

func (m *model) submitPending() tea.Cmd {
	text := m.ta.Value()
	if m.pending.kind == inLimits {
		trimmed := strings.TrimSpace(text)
		if strings.EqualFold(trimmed, "q") {
			m.resolve(inputResult{cancelled: true})
			return nil
		}
		fields := strings.Fields(trimmed)
		steps, err1 := fieldInt(fields, 0)
		cost, err2 := fieldFloat(fields, 1)
		if err1 != nil || err2 != nil || steps <= 0 || cost < 0 {
			m.ta.Reset()
			m.statusErr = true
			m.status = "enter '<steps> <cost>' (e.g. '24 5'), or q to end the run"
			m.layoutDirty = true
			return nil
		}
		m.resolve(inputResult{text: fmt.Sprintf("%d %v", steps, cost)})
		return nil
	}
	trimmedText := strings.TrimSpace(text)
	// TUI-side: /m just expands the (already multiline) input box.
	if trimmedText == "/m" {
		m.expanded = true
		m.layout()
		m.ta.Reset()
		m.syncMenu()
		return nil
	}
	// TUI-side: /h opens the help overlay instead of going to the agent.
	if trimmedText == "/h" {
		m.ta.Reset()
		m.menu.close()
		m.dlg = newHelpDialog()
		m.layout()
		return nil
	}
	// TUI-side: /models opens the model picker instead of going to the agent.
	if trimmedText == "/models" {
		m.ta.Reset()
		m.menu.close()
		m.statusErr = false
		m.working = true
		m.status = "loading models…"
		m.layoutDirty = true
		return tea.Batch(m.spinnerCmd(), m.loadModels())
	}
	// TUI-side: /sessions opens the previous-session picker. It is only
	// meaningful before the first task, where the prompt is collected; at
	// agent prompts the same text could be a legitimate reply.
	if trimmedText == "/sessions" && m.pending.kind == inTask {
		m.ta.Reset()
		m.menu.close()
		m.statusErr = false
		m.working = true
		m.status = "loading sessions…"
		m.layoutDirty = true
		return tea.Batch(m.spinnerCmd(), m.loadSessions())
	}
	// TUI-side memory commands never reach the agent.
	if cmd, handled := m.memoryCommand(trimmedText); handled {
		m.ta.Reset()
		m.menu.close()
		return cmd
	}
	// Echo conversation messages into the transcript so the user can see
	// (and select) their own input. Commands in human mode are rendered by
	// the agent; limits are not conversation.
	switch m.pending.kind {
	case inTask, inComment, inReject:
		if t := strings.TrimSpace(text); t != "" {
			m.appendBlock(userBlock{text: t})
		}
	}
	m.resolve(inputResult{text: text})
	return nil
}

func (m *model) resolve(res inputResult) {
	if m.pending == nil {
		return
	}
	reply := m.pending.reply
	m.pending = nil
	m.resetPrompt()
	if reply != nil {
		reply <- res
	}
}

func (m *model) resolveReply(reply chan inputResult, res inputResult) {
	m.resetPrompt()
	if reply != nil {
		reply <- res
	}
}

func (m *model) resetPrompt() {
	m.status = ""
	m.statusErr = false
	m.working = false
	m.menu.close()
	m.ta.Reset()
	m.ta.Placeholder = idlePlaceholder
	m.ta.Focus()
	m.dlg = nil
	m.layout()
}

func (m *model) placeholderFor(kind inputKind) string {
	switch kind {
	case inTask:
		return "describe the task…"
	case inCommand:
		return "your command · /h=help"
	case inComment, inReject:
		return "what should the agent do instead? · ctrl-c=abort"
	}
	return ""
}

// handleMouse implements select-and-copy over the transcript. A press sets
// the anchor, a drag moves the head, and a release copies the selected
// lines. A click without a drag keeps its old meaning: toggle a tool block.
func (m *model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if m.dlg != nil {
		return nil
	}
	row := msg.Y - max(m.headerH, 1) // header rows sit above the transcript
	if row < 0 || row >= m.vp.Height {
		return nil
	}
	line := m.vp.YOffset + row
	switch {
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		m.selecting = true
		m.selAnchor, m.selHead = line, line
		m.applySelection()

	case msg.Action == tea.MouseActionMotion && msg.Button == tea.MouseButtonLeft && m.selecting:
		if line >= 0 && line < len(m.tLines) && line != m.selHead {
			m.selHead = line
			m.applySelection()
		}

	case msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft && m.selecting:
		m.selecting = false
		if m.selAnchor < 0 || m.selAnchor >= len(m.tLines) {
			return nil
		}
		if m.selAnchor == m.selHead {
			line := m.selAnchor // plain click: keep the old toggle behavior
			m.clearSelection()
			m.toggleBlockAt(line)
			return nil
		}
		lo, hi := m.selRange()
		text := m.selectedText(lo, hi)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return copyCmd(text, hi-lo+1)
	}
	return nil
}

// selRange returns the ordered selection bounds; empty range when inactive.
func (m *model) selRange() (lo, hi int) {
	if m.selAnchor < 0 {
		return 1, 0
	}
	lo, hi = m.selAnchor, m.selHead
	if lo > hi {
		lo, hi = hi, lo
	}
	if hi >= len(m.tLines) {
		hi = len(m.tLines) - 1
	}
	if lo < 0 {
		lo = 0
	}
	return lo, hi
}

// selectedText joins the plain text of the selected lines.
func (m *model) selectedText(lo, hi int) string {
	lines := make([]string, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		lines = append(lines, strings.TrimRight(m.tLines[i].plain, " "))
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func (m *model) clearSelection() {
	m.selAnchor, m.selHead = -1, -1
	m.applySelection()
}

// copyCmd pushes text to the system clipboard: OSC52 first (works over SSH
// with capable terminals), then the native clipboard.
func copyCmd(text string, lines int) tea.Cmd {
	return func() tea.Msg {
		// OSC52 only helps when the native clipboard is out of reach (SSH).
		// Emitting it locally races the renderer on os.Stdout, so skip it there.
		if len(text) <= 100_000 && (os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != "") {
			fmt.Fprint(os.Stdout, osc52.New(text).String())
		}
		_ = clipboard.WriteAll(text)
		return copiedMsg(lines)
	}
}

func pluralLines(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// toggleBlockAt expands/collapses the tool block owning the given
// transcript line index.
func (m *model) toggleBlockAt(row int) {
	if row < 0 {
		return
	}
	for i, end := range m.rows {
		if row < end {
			if tb, ok := m.blocks[i].(toolBlock); ok {
				tb.expanded = !tb.expanded
				m.blocks[i] = tb
				m.dirty = true
			}
			return
		}
	}
}

func (m *model) appendBlock(b block) {
	if m.vp.AtBottom() || m.vp.Height == 0 {
		m.stick = true
	}
	m.blocks = append(m.blocks, b)
	m.appendBlockLines(b)
	if m.stick {
		m.vp.GotoBottom()
	}
}

// appendBlockLines renders a single block and appends its transcript lines
// (plus the blank separator) to the caches, without re-rendering the rest of
// the transcript. This keeps transcript growth linear instead of re-rendering
// every block on each append.
func (m *model) appendBlockLines(b block) {
	w := max(m.vp.Width, 1)
	raw := strings.Split(b.render(w, m.st), "\n")
	for _, ln := range raw {
		m.tLines = append(m.tLines, tLine{plain: ansi.Strip(ln), ansi: ln})
	}
	m.tLines = append(m.tLines, tLine{}) // blank line between blocks
	total := len(raw)
	if len(m.rows) > 0 {
		total += m.rows[len(m.rows)-1] + 1
	}
	m.rows = append(m.rows, total)
	m.applySelection()
}

func (m *model) layout() {
	if m.w == 0 {
		return
	}
	m.ta.SetWidth(max(m.w-2, 1))
	m.headerView = m.header()
	m.headerH = max(len(strings.Split(m.headerView, "\n")), 1)
	m.inputH = m.desiredInputH()
	// Budget every transcript-external section so the whole view fits in h
	// rows while keeping at least one transcript row. Trim the optional
	// sections (pending prompt, then status, then the composer) until the view
	// fits; only the status line and composer have a one-row floor.
	statusWant := max(m.statusHeight(), 1)
	pendingWant := m.pendingHeight()
	for i := 0; i < 8; i++ {
		m.statusRows, m.pendingRows = statusWant, pendingWant
		m.ta.SetHeight(m.inputH)
		m.budgetDialog()
		over := m.usedRows() - (m.h - 1)
		if over <= 0 {
			break
		}
		if pendingWant > 0 {
			d := min(over, pendingWant)
			pendingWant -= d
			over -= d
		}
		if over > 0 && statusWant > 1 {
			d := min(over, statusWant-1)
			statusWant -= d
			over -= d
		}
		if over > 0 && m.inputH > 1 {
			d := min(over, m.inputH-1)
			m.inputH -= d
			over -= d
		}
		if over <= 0 {
			break
		}
	}
	m.statusRows, m.pendingRows = statusWant, pendingWant
	m.ta.SetHeight(m.inputH)
	m.budgetDialog()
	// Only a change of transcript width needs the (expensive) block re-render;
	// height-only changes just resize the viewport.
	if m.vp.Width != m.w {
		m.dirty = true
	}
	m.vp.Width = m.w
	m.vp.Height = max(m.h-m.usedRows(), 1)
	// Cache the header and status lines so View() does not re-render them on
	// every frame (the dialog, pending prompt, menu and composer are cheap and
	// rendered fresh, since their state can change between layouts).
	m.statusView = m.statusLine()
	m.layoutDirty = false
}

// budgetDialog tells the modal how many rows the terminal can spare. The
// confirm dialog uses the budget to window a long approval's command list
// instead of letting the sheet grow until the transcript is pushed off
// screen. It is called from layout() once the header and status rows are
// known, and again after those rows are trimmed, so the sheet tracks the
// space that is actually free.
func (m *model) budgetDialog() {
	if m.dlg == nil {
		return
	}
	// Reserve one row for the transcript viewport (its floor in usedRows), so
	// the sheet plus header and status can never exceed h - 1.
	m.dlg.resize(max(m.h-max(m.headerH, 1)-max(m.statusRows, 1)-1, 1))
}

// desiredInputH is the composer height needed to show all of its content.
// The textarea word-wraps, so we ask it (via textareaRows) how many display
// rows the value needs instead of estimating; an underestimate makes the
// textarea scroll and hides the first line. The expanded flag (ctrl+e, /m)
// acts as a minimum height, and the result is capped so the transcript
// viewport always keeps at least one row.
func (m *model) desiredInputH() int {
	w := max(m.w-2, 1) // same width layout() gives the textarea
	rows := textareaRows(m.ta.Value(), w)
	if m.expanded {
		rows = max(rows, 8)
	}
	return min(rows, max(m.h-4, 1))
}

// statusHeight is the number of rows statusLine() needs once wrapped.
func (m *model) statusHeight() int {
	text, _ := m.statusContent()
	if text == "" {
		return 0
	}
	return wrapHeight(text, m.w)
}

// pendingHeight is the number of rows the pending prompt title needs. The
// body wraps at w-2 because the "❯ " prefix occupies two columns.
func (m *model) pendingHeight() int {
	if m.pending == nil {
		return 0
	}
	return max(wrapHeight(m.pending.title, max(m.w-2, 1)), 1)
}

// pendingLine renders the pending prompt title, wrapped and with the "❯ "
// marker on the first row. It is clipped to the row budget assigned by
// layout() so it can never push the view off screen.
func (m *model) pendingLine() string {
	if m.pending == nil {
		return ""
	}
	rows := m.pendingRows
	if rows <= 0 {
		rows = m.pendingHeight()
	}
	lines := strings.Split(wrapText(m.pending.title, max(m.w-2, 1)), "\n")
	if len(lines) > rows {
		lines = lines[:rows]
	}
	lines[0] = m.st.agent.Render("❯ ") + m.st.info.Render(lines[0])
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + m.st.info.Render(lines[i])
	}
	return strings.Join(lines, "\n")
}

// usedRows counts every transcript-external row the view needs, using the row
// budgets layout() assigned.
func (m *model) usedRows() int {
	used := max(m.headerH, 1) + max(m.statusRows, 1)
	if m.dlg != nil {
		used += lipgloss.Height(m.dlg.render(m.w, m.st))
	} else {
		used += m.pendingRows
		used += m.menuHeight()
		used += m.inputH + 2 // composer border
	}
	return used
}

// wrapHeight is the number of display rows text occupies at the given width.
func wrapHeight(text string, width int) int {
	w := max(width, 1)
	if text == "" {
		return 0
	}
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		rows += len(strings.Split(ansi.Wrap(line, w, ""), "\n"))
	}
	return max(rows, 1)
}

// wrapText soft-wraps text to width so it flows onto the next line instead of
// running off the right edge. It wraps on spaces and only breaks mid-word when
// a single token is wider than the available space.
func wrapText(text string, width int) string {
	w := max(width, 1)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if ansi.StringWidth(line) <= w {
			continue
		}
		lines[i] = ansi.Wrap(line, w, "")
	}
	return strings.Join(lines, "\n")
}

func (m *model) renderTranscript() {
	w := max(m.vp.Width, 1)
	rows := make([]int, len(m.blocks))
	lines := make([]tLine, 0, 64)
	total := 0
	for i, b := range m.blocks {
		r := b.render(w, m.st)
		for _, ln := range strings.Split(r, "\n") {
			lines = append(lines, tLine{plain: ansi.Strip(ln), ansi: ln})
		}
		total += strings.Count(r, "\n") + 1
		rows[i] = total
		lines = append(lines, tLine{}) // blank line between blocks
		total++
	}
	m.rows = rows
	m.tLines = lines
	m.viewLines = nil
	m.selStyledLo, m.selStyledHi = -1, -1
	m.dirty = false
	m.applySelection()
	if m.stick {
		m.vp.GotoBottom()
	}
}

// applySelection rebuilds the viewport content from the cached transcript
// lines, highlighting the selected range. Only the lines whose selection state
// changed are restyled; the styled-line cache is reused across drag events.
func (m *model) applySelection() {
	if len(m.tLines) == 0 {
		m.viewLines = m.viewLines[:0]
		m.selStyledLo, m.selStyledHi = -1, -1
		m.vp.SetContent("")
		return
	}
	// Keep the styled cache in step with the transcript. It only grows on
	// append; a shorter cache means the transcript was rebuilt from scratch.
	if len(m.viewLines) > len(m.tLines) {
		m.viewLines = make([]string, len(m.tLines))
		for i := range m.tLines {
			m.viewLines[i] = m.tLines[i].ansi
		}
		m.selStyledLo, m.selStyledHi = -1, -1
	}
	for len(m.viewLines) < len(m.tLines) {
		i := len(m.viewLines)
		m.viewLines = append(m.viewLines, m.tLines[i].ansi)
	}

	lo, hi := m.selRange()
	// Un-highlight lines that were selected before but no longer are.
	for i := m.selStyledLo; i <= m.selStyledHi && i >= 0; i++ {
		if i < len(m.viewLines) && (i < lo || i > hi) {
			m.viewLines[i] = m.tLines[i].ansi
		}
	}
	for i := lo; i <= hi; i++ {
		txt := m.tLines[i].plain
		if strings.TrimSpace(txt) == "" {
			txt = " "
		}
		m.viewLines[i] = m.st.selLine.Render(txt)
	}
	m.selStyledLo, m.selStyledHi = lo, hi
	m.vp.SetContent(strings.Join(m.viewLines, "\n"))
}

func (m *model) busy() bool {
	return m.working && !m.done && m.pending == nil && m.dlg == nil
}

// statusContent returns the status text (without style) and the style it
// should be rendered in. Keeping the raw text in one place lets statusHeight
// measure exactly what statusLine draws.
func (m *model) statusContent() (string, lipgloss.Style) {
	switch {
	case m.busy():
		text := m.sp.View() + " " + m.status
		if !m.statusSince.IsZero() {
			if d := time.Since(m.statusSince); d >= time.Second {
				text += " (" + shortDuration(d) + ")"
			}
		}
		return text, m.st.info
	case m.status == "":
		return "ready", m.st.faint
	case m.done && m.doneOk:
		return "● " + m.status, m.st.ok
	case m.statusErr:
		return "● " + m.status, m.st.errStyle
	default:
		return m.status, m.st.dim
	}
}

// statusLine wraps long statuses (e.g. a long /memory listing) so they stay
// inside the terminal instead of running off the right edge. It is clipped to
// the row budget assigned by layout().
func (m *model) statusLine() string {
	text, style := m.statusContent()
	if text == "" {
		return ""
	}
	rows := m.statusRows
	if rows <= 0 {
		rows = max(m.statusHeight(), 1)
	}
	lines := strings.Split(wrapText(text, m.w), "\n")
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m *model) header() string {
	left := m.st.title.Render("stubbs") + " " + m.st.modeBadge(m.mode.String())
	right := m.st.faint.Render(m.opts.Model) +
		m.st.dim.Render(fmt.Sprintf(" · step %d · $%.4f", m.steps, m.cost))
	if pad := m.w - plainWidth(left) - plainWidth(right); pad >= 1 {
		return left + strings.Repeat(" ", pad) + right
	}
	return wrapAt(m.w).Render(left)
}

func (m *model) View() string {
	if m.w == 0 {
		return "loading…"
	}
	// layout() only re-runs when something that affects the row budget changed
	// (window size, status, pending prompt, dialog, composer height). Handlers
	// that mutate those set layoutDirty, so a steady frame does no layout work.
	if m.layoutDirty {
		m.layout()
	}
	if m.dirty {
		m.renderTranscript()
	}
	var bottom []string
	if m.dlg != nil {
		sheet := lipgloss.PlaceHorizontal(m.w, lipgloss.Center, m.dlg.render(m.w, m.st))
		bottom = append(bottom, sheet)
	} else {
		if m.pending != nil && m.pendingRows > 0 {
			bottom = append(bottom, m.pendingLine())
		}
		if menu := m.renderMenu(); menu != "" {
			bottom = append(bottom, menu)
		}
		box := m.st.box
		if m.pending != nil || (!m.done && m.status == "") {
			box = m.st.boxFocus
		}
		bottom = append(bottom, box.Render(m.ta.View()))
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.headerView, m.vp.View(), m.statusView, strings.Join(bottom, "\n"))
}

// shortDuration renders an elapsed duration compactly (e.g. "1m32s", "45s").
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func fieldInt(fields []string, i int) (int, error) {
	if i >= len(fields) {
		return 0, fmt.Errorf("missing field %d", i)
	}
	return strconv.Atoi(fields[i])
}

func fieldFloat(fields []string, i int) (float64, error) {
	if i >= len(fields) {
		return 0, fmt.Errorf("missing field %d", i)
	}
	return strconv.ParseFloat(fields[i], 32)
}
