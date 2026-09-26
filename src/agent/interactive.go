package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/types"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrInterrupted           = errors.New("agent: interrupted")
	ErrStepLimitExceeded     = errors.New("agent: step limit exceeded")
	ErrCostLimitExceeded     = errors.New("agent: cost limit exceeded")
	ErrWallTimeLimitExceeded = errors.New("agent: wall time limit exceeded")
	ErrAborted               = errors.New("agent: aborted")
	ErrLimitsExceeded        = errors.New("agent: limits exceeded")
)

type Mode int

const (
	ModeHuman Mode = iota
	ModeConfirm
	ModeYolo
)

func ParseMode(s string) (Mode, error) {
	switch s {
	case "human":
		return ModeHuman, nil
	case "confirm":
		return ModeConfirm, nil
	case "yolo":
		return ModeYolo, nil
	}
	return ModeConfirm, fmt.Errorf("unknown mode %q (want human, confirm or yolo)", s)
}

func (m Mode) String() string {
	switch m {
	case ModeHuman:
		return "human"
	case ModeConfirm:
		return "confirm"
	case ModeYolo:
		return "yolo"
	}
	return "confirm"
}

var modeCommands = map[string]Mode{
	"/u": ModeHuman,
	"/c": ModeConfirm,
	"/y": ModeYolo,
}

func modeCommand(s string) (Mode, bool) {
	m, ok := modeCommands[s]
	return m, ok
}

type UI interface {
	Info(format string, args ...any)
	Assistant(step int, cost float32, msg types.Message)
	Observation(call types.ToolCall, out types.ExecutionOutput)
	Status(text string)
	ModeChanged(m Mode)
	AskConfirm(commands []string) (string, error)
	AskCommand() (string, error)
	AskComment() (string, error)
	AskExit() (string, error)
	AskNewLimits(curSteps, stepLimit int, curCost, costLimit float32) (steps int, cost float32, ok bool, err error)
}

type InteractiveConfig struct {
	AgentConfig
	Mode             Mode
	WhitelistActions []string // regexes; matching commands skip confirmation
	ConfirmExit      bool
	AutoQuit         bool
}

type InteractiveAgent struct {
	*Agent
	cfg         InteractiveConfig
	ui          UI
	whitelist   []*regexp.Regexp
	interrupted atomic.Bool
	stepMu      sync.Mutex
	stepCancel  context.CancelFunc
}

func NewInteractiveAgent(cfg InteractiveConfig, client llm.ModelClient, environ env.Environment, model string, ui UI) (*InteractiveAgent, error) {
	if ui == nil {
		return nil, fmt.Errorf("agent: interactive agent requires a UI")
	}
	base, err := NewAgent(&cfg.AgentConfig, client, environ, model, false)
	if err != nil {
		return nil, err
	}
	ia := &InteractiveAgent{Agent: base, cfg: cfg, ui: ui}
	for _, pattern := range cfg.WhitelistActions {
		re, err := regexp.Compile(pattern)
		if err != nil {
			base.Close()
			return nil, fmt.Errorf("whitelist %q: %w", pattern, err)
		}
		ia.whitelist = append(ia.whitelist, re)
	}
	return ia, nil
}

func (ia *InteractiveAgent) Mode() Mode { return ia.cfg.Mode }

// SetMode switches the interaction mode and reports the change to the UI.
func (ia *InteractiveAgent) SetMode(m Mode) {
	if ia.cfg.Mode == m {
		ia.ui.Info("Already in %s mode.", m)
		return
	}
	ia.cfg.Mode = m
	ia.ui.ModeChanged(m)
	ia.ui.Info("Switched to %s mode.", m)
}

func (ia *InteractiveAgent) Run(ctx context.Context, task string) (string, error) {
	defer ia.Close()
	a := ia.Agent
	if a.config.WallTimeLimit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Second*time.Duration(a.config.WallTimeLimit))
		defer cancel()
	}

	if err := a.appendMessage(types.Message{Role: types.RoleUser, Content: task}); err != nil {
		return "", err
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := ia.checkBudget(); err != nil {
			return "", err
		}
		if ia.cfg.Mode == ModeHuman {
			cmd, ok, err := ia.humanCommand()
			if err != nil {
				cont, err := ia.promptErr(err)
				if err != nil {
					return "", err
				}
				if cont {
					continue
				}
				return "", nil
			}
			if ok {
				if err := ia.userExecute(ctx, cmd); err != nil {
					return "", err
				}
				if err := ia.maybeComment(); err != nil {
					return "", err
				}
				continue
			}
		}
		msg, err := ia.modelStep(ctx)
		if err != nil {
			if ia.takeInterrupt() {
				if cerr := ia.interruptComment(); cerr != nil {
					return "", cerr
				}
				continue
			}
			return "", err
		}
		ia.ui.Assistant(a.Steps, a.Cost, msg)
		if len(msg.ToolCalls) == 0 {
			done, err := ia.finish(msg.Content)
			if err != nil {
				cont, rerr := ia.promptErr(err)
				if rerr != nil {
					return "", rerr
				}
				if cont {
					continue
				}
				return "", err
			}
			if done {
				return msg.Content, nil
			}
			continue
		}
		approved, err := ia.confirmCalls(msg.ToolCalls)
		if err != nil {
			// TODO: this block is repeated way too much
			cont, rerr := ia.promptErr(err)
			if rerr != nil {
				return "", rerr
			}
			if cont {
				continue
			}
			return "", err
		}
		if !approved {
			continue
		}
		ia.ui.Status(fmt.Sprintf("running %s...", plural(len(msg.ToolCalls), "command")))
		// Register a cancel for tool execution too, so ESC/ctrl-c can stop a
		// long-running command instead of leaving the UI apparently frozen.
		runCtx, cancelRuns := context.WithCancel(ctx)
		ia.setStepCancel(cancelRuns)
		outputs, err := a.executeRuns(runCtx, msg.ToolCalls)
		ia.setStepCancel(nil)
		cancelRuns()
		if err != nil {
			return "", err
		}
		for i, call := range msg.ToolCalls {
			ia.ui.Observation(call, outputs[i])
		}
		if err := ia.maybeComment(); err != nil {
			return "", err
		}
	}

}

func (ia *InteractiveAgent) modelStep(ctx context.Context) (types.Message, error) {
	for {
		ia.ui.Status("waiting for LLM...")
		sctx, cancel := context.WithCancel(ctx)
		ia.setStepCancel(cancel)
		msg, err := ia.Agent.respond(sctx)
		cancel()
		ia.setStepCancel(nil)
		if err == nil {
			return msg, nil
		}
		if ctx.Err() != nil {
			return types.Message{}, ctx.Err()
		}
		if ia.takeInterrupt() {
			if cerr := ia.interruptComment(); cerr != nil {
				return types.Message{}, cerr
			}
			continue
		}
		return types.Message{}, err
	}
}

// WHY WOULD A USER ISSUE A COMMAND HIMSELF?
// userExecute runs a user-issued command in human mode. The command is
// recorded as an assistant tool call so the observation has a matching
// tool_call_id in the API history.
func (ia *InteractiveAgent) userExecute(ctx context.Context, cmd string) error {
	a := ia.Agent
	call := types.ToolCall{
		ID:       fmt.Sprintf("user-%d", a.ModelCalls+1),
		Type:     "function",
		Function: types.FunctionCall{Name: "bash", Arguments: marshalCommand(cmd)},
	}
	assistant := types.Message{
		Role:      types.RoleAssistant,
		Content:   fmt.Sprintf("User command:\n```bash\n%s\n```", cmd),
		ToolCalls: []types.ToolCall{call},
	}
	if err := a.appendMessage(assistant); err != nil {
		return err
	}
	ia.ui.Assistant(a.Steps, a.Cost, assistant)
	ia.ui.Status("running your command...")
	out := a.Environment.Execute(ctx, call)
	ia.ui.Observation(call, out)
	return a.appendMessage(types.Message{
		Role:       types.RoleTool,
		Content:    renderExecution(out),
		ToolCallID: call.ID,
		Name:       call.Function.Name,
	})
}

func marshalCommand(cmd string) string {
	b, err := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: cmd})
	if err != nil {
		return `{"command": ""}`
	}
	return string(b)
}

func (ia *InteractiveAgent) checkBudget() error {
	if ia.cfg.StepLimit <= 0 && ia.cfg.CostLimit <= 0 {
		return nil
	}
	for ia.exceeded() {
		if ia.cfg.AutoQuit {
			return ErrLimitsExceeded
		}
		ia.ui.Info("Limits exceeded. Limits: %d steps, $%.2f. Current spend: %d steps, $%.4f.",
			ia.cfg.StepLimit, ia.cfg.CostLimit, ia.Steps, ia.Cost)
		steps, cost, ok, err := ia.ui.AskNewLimits(ia.Steps, ia.cfg.StepLimit, ia.Cost, ia.cfg.CostLimit)
		if err != nil {
			return err
		}
		if !ok {
			return ErrLimitsExceeded
		}
		if steps > 0 {
			ia.cfg.StepLimit = steps
		}
		if cost > 0 {
			ia.cfg.CostLimit = cost
		}
	}
	return nil
}

func (ia *InteractiveAgent) exceeded() bool {
	if ia.cfg.StepLimit > 0 && ia.Steps >= ia.cfg.StepLimit {
		return true
	}
	return ia.cfg.CostLimit > 0 && ia.Cost > ia.cfg.CostLimit
}

func (ia *InteractiveAgent) humanCommand() (cmd string, ok bool, err error) {
	for {
		ia.ui.Status("human mode — type the next command")
		input, err := ia.ui.AskCommand()
		if err != nil {
			return "", false, err
		}
		input = strings.TrimSpace(input)
		switch {
		case input == "":
			continue
		case input == "/h":
			ia.printHelp()
		case input == "/m":
			ia.ui.Info("Input is multiline already; use ctrl+e to expand it.")
		case input == "/u":
			ia.SetMode(ModeHuman) // prints "Already in human mode."
		default:
			if m, isMode := modeCommand(input); isMode {
				ia.SetMode(m)
				return "", false, nil // switching mode hands control back to the model
			}
			if strings.HasPrefix(input, "/") {
				ia.printHelp()
				continue
			}
			return input, true, nil
		}
	}
}

// Interrupt is called by the UI when the user pressed Ctrl-C while the agent
// was busy. It cancels the in-flight model call or tool execution; the agent
// then asks for a comment instead of aborting.
func (ia *InteractiveAgent) Interrupt() {
	ia.interrupted.Store(true)
	ia.stepMu.Lock()
	cancel := ia.stepCancel
	ia.stepMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// setStepCancel records the cancel func of the operation currently in flight
// (a model call or a batch of tool executions) so Interrupt can cancel it.
func (ia *InteractiveAgent) setStepCancel(cancel context.CancelFunc) {
	ia.stepMu.Lock()
	ia.stepCancel = cancel
	ia.stepMu.Unlock()
}

func (ia *InteractiveAgent) takeInterrupt() bool {
	return ia.interrupted.CompareAndSwap(true, false)
}

func (ia *InteractiveAgent) promptErr(err error) (bool, error) {
	if errors.Is(err, ErrInterrupted) {
		if cerr := ia.interruptComment(); cerr != nil {
			return false, cerr
		}
		return true, nil
	}
	return false, err
}

func (ia *InteractiveAgent) maybeComment() error {
	if !ia.takeInterrupt() {
		return nil
	}
	return ia.interruptComment()
}

func (ia *InteractiveAgent) interruptComment() error {
	for {
		text, err := ia.ui.AskComment()
		if err != nil {
			return err //ErrAborted: user pressed Ctrl+C
		}
		text = strings.TrimSpace(text)
		switch {
		case text == "/h":
			ia.printHelp()
			continue
		case text == "/m":
			ia.ui.Info("Input is multiline already; use ctrl+e to expand it.")
			continue
		}
		if m, isMode := modeCommand(text); isMode {
			ia.SetMode(m)
			text = "Temporary interruption caught."
		}
		return ia.appendMessage(types.Message{
			Role:    types.RoleUser,
			Content: fmt.Sprintf("Interrupted by user: %s", text),
		})
	}
}

func (ia *InteractiveAgent) confirmCalls(calls []types.ToolCall) (bool, error) {
	if ia.cfg.Mode != ModeConfirm {
		return true, nil
	}
	commands := make([]string, 0, len(calls))
	needsConfirm := false
	for _, call := range calls {
		cmd := CommandOf(call)
		commands = append(commands, cmd)
		if !ia.whitelisted(cmd) {
			needsConfirm = true
		}
	}
	if !needsConfirm {
		return true, nil
	}
	for {
		ia.ui.Status("confirm action")
		text, err := ia.ui.AskConfirm(commands)
		if err != nil {
			return false, err
		}
		text = strings.TrimSpace(text)
		switch {
		case text == "":
			return true, nil
		case text == "/y":
			ia.SetMode(ModeYolo)
			return true, nil
		case text == "/c":
			ia.SetMode(ModeConfirm) // prints "Already in confirm mode."
		case text == "/h":
			ia.printHelp()
		case text == "/m":
			ia.ui.Info("Input is multiline already; use ctrl+e to expand it.")
		case text == "/u":
			ia.SetMode(ModeHuman)
			if err := ia.appendMessage(types.Message{
				Role:    types.RoleUser,
				Content: "Commands not executed. Switching to human mode",
			}); err != nil {
				return false, err
			}
			return false, nil
		case strings.HasPrefix(text, "/"):
			ia.printHelp()
		default:
			if err := ia.appendMessage(types.Message{
				Role:    types.RoleUser,
				Content: fmt.Sprintf("Commands not executed. The user rejected your commands with the following message: %s", text),
			}); err != nil {
				return false, err
			}
			return false, nil
		}
	}
}

func (ia *InteractiveAgent) whitelisted(cmd string) bool {
	for _, re := range ia.whitelist {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// CommandOf extracts the human-readable command from a tool call.
func CommandOf(call types.ToolCall) string {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err == nil && args.Command != "" {
		return args.Command
	}
	return call.Function.Arguments
}

func (ia *InteractiveAgent) finish(content string) (bool, error) {
	if !ia.cfg.ConfirmExit {
		return true, nil
	}
	for {
		ia.ui.Status("agent wants to finish")
		text, err := ia.ui.AskExit()
		if err != nil {
			return false, err
		}
		text = strings.TrimSpace(text)
		switch {
		case text == "":
			return true, nil
		case text == "/h":
			ia.printHelp()
		case text == "/m":
			ia.ui.Info("Input is multiline already; use ctrl+e to expand it.")
		case text == "/u":
			ia.cfg.Mode = ModeHuman
			ia.ui.ModeChanged(ModeHuman)
			if err := ia.appendMessage(types.Message{
				Role:    types.RoleUser,
				Content: "Switched to human mode.",
			}); err != nil {
				return false, err
			}
			return false, nil
		default:
			if m, isMode := modeCommand(text); isMode {
				ia.SetMode(m) // /y, /c: switch and ask again
				continue
			}
			if strings.HasPrefix(text, "/") {
				ia.printHelp()
				continue
			}
			if err := ia.appendMessage(types.Message{
				Role:    types.RoleUser,
				Content: fmt.Sprintf("The user added a new task: %s", text),
			}); err != nil {
				return false, err
			}
			return false, nil
		}
	}
}

func (ia *InteractiveAgent) printHelp() {
	ia.ui.Info("Current mode: %s", ia.cfg.Mode)
	ia.ui.Info("/y — switch to yolo mode (execute LM commands without confirmation)")
	ia.ui.Info("/c — switch to confirm mode (ask before executing LM commands)")
	ia.ui.Info("/u — switch to human mode (execute commands issued by the user)")
	ia.ui.Info("/m — expand the input box (multiline editing)")
	ia.ui.Info("/h — show this help")
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
