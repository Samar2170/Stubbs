package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

const idlePlaceholder = "Type here…  (/h for help)"

type Options struct {
	Model    string
	AutoQuit bool
	Theme    string
	Mode     agent.Mode
}

// App implements agent.UI on top of a full-screen Bubble Tea program.
// Ask* methods block on a reply channel that the Update loop resolves.
type App struct {
	prog      *tea.Program
	interrupt func()
	autoQuit  bool
}

func New(opts Options) *App {
	m := &model{
		opts:      opts,
		st:        newStyles(loadTheme(opts.Theme)),
		mode:      opts.Mode,
		showTools: true,
	}
	m.vp = viewport.New(80, 24)
	m.ta = newTextarea()
	m.sp = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	a := &App{prog: p, autoQuit: opts.AutoQuit}
	m.app = a
	return a
}

// SetInterrupt wires Ctrl-C to the agent (call after the agent is built).
func (a *App) SetInterrupt(f func()) { a.interrupt = f }

// Run runs the TUI until the user quits.
func (a *App) Run() error {
	_, err := a.prog.Run()
	return err
}

// AwaitTask blocks until the user submits the initial task.
func (a *App) AwaitTask() (string, error) {
	return a.ask(inTask, "What do you want to do?")
}

// Finish reports the run outcome and lets the user quit with ctrl-c.
func (a *App) Finish(submission string, err error) {
	summary, ok := finishSummary(err)
	a.prog.Send(doneMsg{summary: summary, ok: ok})
	if s := strings.TrimSpace(submission); s != "" {
		a.prog.Send(blockMsg{userBlock{text: s}})
	}
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
	res := <-reply
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
	res := <-reply
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
	res := <-reply
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

type model struct {
	opts      Options
	app       *App
	st        styles
	w, h      int
	inputH    int
	blocks    []block
	rows      []int // cumulative transcript line count per block
	stick     bool  // follow the bottom of the transcript
	showTools bool  // global expand/collapse for tool output (start expanded)
	dirty     bool
	vp        viewport.Model
	ta        textarea.Model
	sp        spinner.Model
	status    string
	statusErr bool
	doneOk    bool
	mode      agent.Mode
	steps     int
	cost      float32
	pending   *pendingInput
	dlg       *dialog
	expanded  bool
	done      bool
}

func newTextarea() textarea.Model {
	ta := textarea.New()
	ta.Placeholder = idlePlaceholder
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.Focus()
	return ta
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.sp.Tick)
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
		return m, nil

	case headerMsg:
		m.steps, m.cost = msg.steps, msg.cost
		return m, nil

	case modeMsg:
		m.mode = agent.Mode(msg)
		return m, nil

	case inputReqMsg:
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
		return m, textarea.Blink

	case limitsReqMsg:
		m.pending = &pendingInput{kind: inLimits, title: "Raise limits", reply: msg.reply}
		m.ta.Placeholder = fmt.Sprintf("new limits, e.g. '%d %.2f'  ·  q ends the run", msg.stepLimit, msg.costLimit)
		m.ta.Reset()
		m.ta.Focus()
		return m, textarea.Blink

	case doneMsg:
		m.done = true
		m.pending = nil
		m.dlg = nil
		m.status = msg.summary
		m.doneOk = msg.ok
		m.statusErr = !msg.ok
		m.ta.Reset()
		m.ta.Blur()
		if msg.ok {
			m.appendBlock(okBlock{text: msg.summary})
		} else {
			m.appendBlock(errorBlock{text: msg.summary})
		}
		return m, nil

	case autoQuitMsg:
		return m, tea.Quit

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		if msg.Type == tea.MouseLeft && msg.Action == tea.MouseActionPress {
			m.toggleBlockAt(msg.Y - 1) // -1: header row
		}
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd
	if m.dlg == nil {
		m.ta, cmd = m.ta.Update(msg)
		cmds = append(cmds, cmd)
	}
	m.vp, cmd = m.vp.Update(msg)
	cmds = append(cmds, cmd)
	if !m.vp.AtBottom() {
		m.stick = false // the user scrolled up; stop auto-follow
	}
	return m, tea.Batch(cmds...)
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dlg != nil {
		return m.handleDialogKey(msg.String())
	}
	switch msg.String() {
	case "ctrl+c":
		if m.done {
			return m, tea.Quit
		}
		return m.interruptKey()

	case "esc":
		if m.done {
			return m, nil
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
			m.submitPending()
			return m, nil
		}
		if !m.done {
			m.status = "agent is busy — ctrl-c interrupts"
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg) // ctrl+j inserts a newline
	return m, cmd
}

func (m *model) handleDialogKey(key string) (tea.Model, tea.Cmd) {
	d := m.dlg
	switch key {
	case "ctrl+c":
		if m.done {
			return m, tea.Quit
		}
		if d.reply != nil {
			m.resolveReply(d.reply, inputResult{interrupted: true})
		} else {
			m.dlg = nil
		}
		return m, nil

	case "esc":
		switch d.kind {
		case dlgHelp:
			m.dlg = nil
		case dlgConfirm:
			m.swapComposer(inReject, "Rejecting — what went wrong?", "what should the agent do instead? · ctrl-c=abort")
		default: // exit: esc finishes
			m.resolveReply(d.reply, inputResult{})
		}
		return m, nil

	case "up", "k":
		d.selected = (d.selected + len(d.options) - 1) % len(d.options)
		return m, nil

	case "down", "j", "tab":
		d.selected = (d.selected + 1) % len(d.options)
		return m, nil

	case "enter":
		if d.kind == dlgHelp {
			m.dlg = nil
			return m, nil
		}
		m.pickDialogOption()
		return m, nil

	case "y":
		if d.kind != dlgHelp {
			d.selected = 0
			m.pickDialogOption()
		}
		return m, nil

	case "n":
		if d.kind == dlgExit {
			d.selected = 1
			m.pickDialogOption()
		}
		return m, nil

	case "h":
		if d.kind == dlgExit {
			d.selected = 2
			m.pickDialogOption()
		}
		return m, nil

	case "q":
		if d.kind == dlgHelp {
			m.dlg = nil
		}
		return m, nil
	}
	return m, nil // the modal swallows everything else
}

func (m *model) pickDialogOption() {
	d := m.dlg
	if d.selected >= len(d.options) {
		d.selected = 0
	}
	o := d.options[d.selected]
	switch o.action {
	case dlgReject:
		m.swapComposer(inReject, "Rejecting — what went wrong?", m.placeholderFor(inReject))
	case dlgNewTask:
		m.swapComposer(inTask, "New task", m.placeholderFor(inTask))
	default:
		m.resolveReply(d.reply, inputResult{text: o.text})
	}
}

// swapComposer turns the active dialog into a composer prompt that answers
// the same pending request.
func (m *model) swapComposer(kind inputKind, title string, placeholder string) {
	m.pending = &pendingInput{kind: kind, title: title, reply: m.dlg.reply}
	m.dlg = nil
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
	}
	return m, nil
}

func (m *model) submitPending() {
	text := m.ta.Value()
	if m.pending.kind == inLimits {
		trimmed := strings.TrimSpace(text)
		if strings.EqualFold(trimmed, "q") {
			m.resolve(inputResult{cancelled: true})
			return
		}
		fields := strings.Fields(trimmed)
		steps, err1 := fieldInt(fields, 0)
		cost, err2 := fieldFloat(fields, 1)
		if err1 != nil || err2 != nil || steps <= 0 || cost < 0 {
			m.ta.Reset()
			m.statusErr = true
			m.status = "enter '<steps> <cost>' (e.g. '24 5'), or q to end the run"
			return
		}
		m.resolve(inputResult{text: fmt.Sprintf("%d %v", steps, cost)})
		return
	}
	// TUI-side: /m just expands the (already multiline) input box.
	if strings.TrimSpace(text) == "/m" {
		m.expanded = true
		m.layout()
		m.ta.Reset()
		return
	}
	// TUI-side: /h opens the help overlay instead of going to the agent.
	if strings.TrimSpace(text) == "/h" {
		m.ta.Reset()
		m.dlg = newHelpDialog()
		m.layout()
		return
	}
	m.resolve(inputResult{text: text})
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
	m.ta.Reset()
	m.ta.Placeholder = idlePlaceholder
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

func (m *model) toggleBlockAt(y int) {
	if y < 0 {
		return
	}
	row := m.vp.YOffset + y
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
	m.dirty = true
}

func (m *model) layout() {
	if m.w == 0 {
		return
	}
	m.inputH = 1
	if m.expanded {
		m.inputH = 8
	}
	m.vp.Width = m.w
	m.vp.Height = max(m.h-m.usedRows(), 1)
	m.ta.SetWidth(max(m.w-2, 1))
	m.ta.SetHeight(m.inputH)
	m.dirty = true
}

// usedRows counts every transcript-external row the view needs.
func (m *model) usedRows() int {
	used := 2 // header + status line
	if m.dlg != nil {
		used += lipgloss.Height(m.dlg.render(m.w, m.st))
	} else {
		if m.pending != nil {
			used++
		}
		used += m.inputH + 2 // composer border
	}
	return used
}

func (m *model) renderTranscript() {
	w := max(m.vp.Width, 1)
	rows := make([]int, len(m.blocks))
	var parts []string
	total := 0
	for i, b := range m.blocks {
		r := b.render(w, m.st)
		total += strings.Count(r, "\n") + 1
		rows[i] = total
		parts = append(parts, r, "") // blank line between blocks
	}
	m.rows = rows
	content := strings.Join(parts, "\n")
	m.vp.SetContent(strings.TrimSuffix(content, "\n"))
	if m.stick {
		m.vp.GotoBottom()
	}
	m.dirty = false
}

func (m *model) busy() bool {
	return !m.done && m.pending == nil && m.dlg == nil && m.status != ""
}

func (m *model) statusLine() string {
	if m.busy() {
		return m.sp.View() + " " + m.st.info.Render(m.status)
	}
	if m.status == "" {
		return m.st.faint.Render("ready")
	}
	if m.done && m.doneOk {
		return m.st.ok.Render("● " + m.status)
	}
	if m.statusErr {
		return m.st.errStyle.Render("● " + m.status)
	}
	return m.st.dim.Render(m.status)
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
	if m.dirty {
		m.renderTranscript()
	}
	var bottom []string
	if m.dlg != nil {
		sheet := lipgloss.PlaceHorizontal(m.w, lipgloss.Center, m.dlg.render(m.w, m.st))
		bottom = append(bottom, sheet)
	} else {
		if m.pending != nil {
			bottom = append(bottom, m.st.agent.Render("❯ ")+m.st.info.Render(m.pending.title))
		}
		box := m.st.box
		if m.pending != nil || (!m.done && m.status == "") {
			box = m.st.boxFocus
		}
		bottom = append(bottom, box.Render(m.ta.View()))
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), m.vp.View(), m.statusLine(), strings.Join(bottom, "\n"))
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
