package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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
)

type inputResult struct {
	text        string
	interrupted bool // ctrl-c while this prompt was pending
	aborted     bool // ctrl-c at comment/limits prompt: abort the run
	cancelled   bool // "q" at the limits prompt: end the run
}

type pendingInput struct {
	kind  inputKind
	title string
	reply chan inputResult
}

type lineMsg string
type statusMsg string
type headerMsg struct {
	steps int
	cost  float32
}
type modeMsg agent.Mode
type inputReqMsg struct {
	kind  inputKind
	title string
	reply chan inputResult
}
type limitsReqMsg struct {
	curSteps, stepLimit int
	curCost, costLimit  float32
	reply               chan inputResult
}
type doneMsg string

var (
	agentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	userStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	toolStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	infoStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
)

const idlePlaceholder = "Type here…  (/h for help)"

type Options struct {
	Model string
}

// App implements agent.UI on top of a full-screen Bubble Tea program.
// Ask* methods block on a reply channel that the Update loop resolves.
type App struct {
	prog      *tea.Program
	interrupt func()
}

func New(opts Options) *App {
	m := &model{opts: opts}
	m.vp = viewport.New(80, 24)
	m.ta = newTextarea()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	a := &App{prog: p}
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
	var summary string
	switch {
	case err == nil:
		summary = "Run complete."
	case errors.Is(err, agent.ErrAborted):
		summary = "Run aborted by user."
	case errors.Is(err, agent.ErrLimitsExceeded):
		summary = "Run ended: limits exceeded."
	case errors.Is(err, context.Canceled):
		summary = "Run canceled."
	default:
		summary = "Run failed: " + err.Error()
	}
	a.prog.Send(doneMsg(summary))
	a.prog.Send(lineMsg(dimStyle.Render(strings.TrimSpace(submission))))
}

func (a *App) ask(kind inputKind, title string) (string, error) {
	reply := make(chan inputResult, 1)
	a.prog.Send(inputReqMsg{kind: kind, title: title, reply: reply})
	res := <-reply
	switch {
	case res.aborted || res.cancelled:
		return "", agent.ErrAborted
	case res.interrupted:
		return "", agent.ErrInterrupted
	}
	return res.text, nil
}

func (a *App) Info(format string, args ...any) {
	a.prog.Send(lineMsg(infoStyle.Render("• " + fmt.Sprintf(format, args...))))
}

func (a *App) Assistant(step int, cost float32, msg types.Message) {
	a.prog.Send(headerMsg{steps: step, cost: cost})
	a.prog.Send(lineMsg(agentStyle.Render(fmt.Sprintf("stubbs (step %d, $%.4f):", step, cost))))
	if strings.TrimSpace(msg.Content) != "" {
		a.prog.Send(lineMsg(msg.Content))
	}
	for _, call := range msg.ToolCalls {
		a.prog.Send(lineMsg(userStyle.Render("→ " + agent.CommandOf(call))))
	}
}

func (a *App) Observation(call types.ToolCall, out types.ExecutionOutput) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s exit=%d %s",
		toolStyle.Render(call.Function.Name),
		out.Code,
		dimStyle.Render(out.Duration.Round(time.Millisecond).String()))
	if strings.TrimSpace(out.Output) != "" {
		b.WriteString("\n" + clip(out.Output, 60))
	}
	if out.Error != "" {
		b.WriteString("\n" + infoStyle.Render(clip(out.Error, 10)))
	}
	a.prog.Send(lineMsg(b.String()))
}

func (a *App) Status(text string) { a.prog.Send(statusMsg(text)) }

func (a *App) ModeChanged(m agent.Mode) { a.prog.Send(modeMsg(m)) }

func (a *App) AskConfirm(commands []string) (string, error) {
	title := fmt.Sprintf("Execute %d command(s)?  enter=approve · text=reject · /h=help", len(commands))
	return a.ask(inConfirm, title)
}

func (a *App) AskCommand() (string, error) { return a.ask(inCommand, "Your command") }

func (a *App) AskComment() (string, error) {
	return a.ask(inComment, "Comment (ctrl-c again to abort)")
}

func (a *App) AskExit() (string, error) {
	return a.ask(inExit, "Agent wants to finish · enter=submit · text=new task · /u=human mode")
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
	opts     Options
	app      *App
	width    int
	height   int
	lines    []string
	vp       viewport.Model
	ta       textarea.Textarea
	status   string
	mode     agent.Mode
	steps    int
	cost     float32
	pending  *pendingInput
	expanded bool
	done     bool
}

func newTextarea() textarea.Textarea {
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

func (m *model) Init() tea.Cmd { return textarea.Blink }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case lineMsg:
		m.lines = append(m.lines, string(msg))
		m.syncViewport()
		return m, nil

	case statusMsg:
		m.status = string(msg)
		return m, nil

	case headerMsg:
		m.steps, m.cost = msg.steps, msg.cost
		return m, nil

	case modeMsg:
		m.mode = agent.Mode(msg)
		return m, nil

	case inputReqMsg:
		m.pending = &pendingInput{kind: msg.kind, title: msg.title, reply: msg.reply}
		m.ta.Placeholder = m.placeholderFor(msg.kind)
		m.ta.Reset()
		m.ta.Focus()
		return m, textarea.Blink

	case limitsReqMsg:
		m.pending = &pendingInput{kind: inLimits, title: "Raise limits", reply: msg.reply}
		m.ta.Placeholder = fmt.Sprintf("new limits, e.g. '%d %.2f'  ·  q to end run", msg.stepLimit, msg.costLimit)
		m.ta.Reset()
		m.ta.Focus()
		return m, textarea.Blink

	case doneMsg:
		m.done = true
		m.pending = nil
		m.status = string(msg)
		m.ta.Reset()
		m.ta.Blur()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	cmds = append(cmds, cmd)
	m.vp, cmd = m.vp.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.done {
			return m, tea.Quit
		}
		if m.pending != nil {
			res := inputResult{interrupted: true}
			if m.pending.kind == inComment || m.pending.kind == inLimits {
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

	case "enter":
		if m.pending != nil {
			m.submitPending()
			return m, nil
		}
		m.status = "agent is busy — press ctrl-c to interrupt"
		return m, nil

	case "ctrl+e":
		m.expanded = !m.expanded
		m.layout()
		return m, nil
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg) // ctrl+j inserts a newline
	return m, cmd
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
			m.lines = append(m.lines, infoStyle.Render("• Enter '<steps> <cost>' (e.g. '24 5'), or q to end the run."))
			m.syncViewport()
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
	m.resolve(inputResult{text: text})
}

func (m *model) resolve(res inputResult) {
	if m.pending == nil {
		return
	}
	reply := m.pending.reply
	m.pending = nil
	m.status = ""
	m.ta.Reset()
	m.ta.Placeholder = idlePlaceholder
	m.layout()
	if reply != nil {
		reply <- res
	}
}

func (m *model) placeholderFor(kind inputKind) string {
	switch kind {
	case inConfirm:
		return "enter=approve · text=reject comment · /h=help"
	case inCommand:
		return "your command · /h=help"
	case inComment:
		return "what should the agent do instead? · ctrl-c=abort"
	case inExit:
		return "enter=submit · text=new task · /u=human mode"
	case inTask:
		return "What do you want to do?"
	}
	return ""
}

func (m *model) layout() {
	if m.width == 0 {
		return
	}
	inputH := 1
	if m.expanded {
		inputH = 8
	}
	// header + prompt title + input box (border adds 2) + status + hint
	used := 1 + 1 + inputH + 2 + 1 + 1
	m.vp.Width = m.width
	m.vp.Height = max(m.height-used, 1)
	m.ta.SetWidth(max(m.width-4, 1))
	m.ta.SetHeight(inputH)
	m.syncViewport()
}

func (m *model) syncViewport() {
	wrap := lipgloss.NewStyle().Width(max(m.vp.Width, 1))
	rendered := make([]string, len(m.lines))
	for i, line := range m.lines {
		rendered[i] = wrap.Render(line)
	}
	m.vp.SetContent(strings.Join(rendered, "\n"))
	m.vp.GotoBottom()
}

func (m *model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	header := titleStyle.Render(" stubbs ") +
		dimStyle.Render(fmt.Sprintf("· mode %s · step %d · $%.4f · %s", m.mode, m.steps, m.cost, m.opts.Model))
	title := ""
	if m.pending != nil {
		title = infoStyle.Render(m.pending.title)
	}
	status := m.status
	if strings.TrimSpace(status) == "" {
		status = "ready"
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.vp.View(),
		title,
		dimStyle.Render(status),
		boxStyle.Render(m.ta.View()),
		dimStyle.Render("enter submit · ctrl+j newline · ctrl+e expand · /h help · ctrl-c interrupt"),
	)
}

func clip(s string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= maxLines {
		return s
	}
	head := maxLines * 2 / 3
	tail := maxLines - head
	hidden := len(lines) - head - tail
	out := append([]string{}, lines[:head]...)
	out = append(out, dimStyle.Render(fmt.Sprintf("… [%d lines hidden] …", hidden)))
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
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
