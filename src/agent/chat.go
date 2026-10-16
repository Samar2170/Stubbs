package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/types"
)

// ChatUI receives events from a chat turn. Unlike UI it has no approval or
// mode prompts: a chat turn always runs its tools and returns the model's
// final text.
type ChatUI interface {
	Status(text string)
	Assistant(step int, cost float32, msg types.Message)
	Observation(call types.ToolCall, out types.ExecutionOutput)
}

// CHAT_SYSTEM_PROMPT steers the shared agent loop toward conversation and
// research rather than acting on the local machine.
const CHAT_SYSTEM_PROMPT = "You are a helpful, conversational assistant. " +
	"Answer the user directly and concisely. When you need current or " +
	"factual information, use the web tool to look it up and cite the URL " +
	"you used. If a question does not need the web, just answer."

// ChatConfig controls a chat session's budgets.
type ChatConfig struct {
	// StepLimit caps the number of model calls in a single turn (0 = no cap).
	StepLimit int
	// CostLimit caps the cumulative session cost in dollars (0 = no cap).
	CostLimit float32
}

// Chat runs a multi-turn conversation on top of the shared agent loop. Each
// Send is one user turn: the user message is appended, the model is called,
// any tool calls are executed, and the loop repeats until the model replies
// with plain text.
type Chat struct {
	*Agent
	cfg         ChatConfig
	ui          ChatUI
	interrupted atomic.Bool
	stepMu      sync.Mutex
	stepCancel  context.CancelFunc
}

// NewChat builds a chat session. It uses the same Agent, LLM client,
// environment and tool registry as the rest of stubbs.
func NewChat(cfg ChatConfig, client llm.ModelClient, environ env.Environment, model string, ui ChatUI) (*Chat, error) {
	if ui == nil {
		return nil, fmt.Errorf("chat: requires a UI")
	}
	base, err := NewAgent(&AgentConfig{
		StepLimit:    cfg.StepLimit,
		CostLimit:    cfg.CostLimit,
		SystemPrompt: CHAT_SYSTEM_PROMPT,
	}, client, environ, model, false)
	if err != nil {
		return nil, err
	}
	return &Chat{Agent: base, cfg: cfg, ui: ui}, nil
}

// Interrupt cancels the model call or tool execution currently in flight.
// The turn returns context.Canceled, which the UI treats as a stopped reply.
func (c *Chat) Interrupt() {
	c.interrupted.Store(true)
	c.stepMu.Lock()
	cancel := c.stepCancel
	c.stepMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Chat) setStepCancel(cancel context.CancelFunc) {
	c.stepMu.Lock()
	c.stepCancel = cancel
	c.stepMu.Unlock()
}

// Send appends the user message and runs the agent loop until the model
// produces a final text reply. Steps are counted per turn; cost accumulates
// across the session.
func (c *Chat) Send(ctx context.Context, text string) (string, error) {
	a := c.Agent
	// Refuse to start a turn that is already over the cumulative cost budget,
	// so we never append a dangling user message with no reply.
	if c.cfg.CostLimit > 0 && a.Cost > c.cfg.CostLimit {
		return "", ErrLimitsExceeded
	}
	if err := a.appendMessage(types.Message{Role: types.RoleUser, Content: text}); err != nil {
		return "", err
	}
	c.interrupted.Store(false)
	a.Steps = 0
	for c.underBudget() {
		if c.takeInterrupt() {
			return "", context.Canceled
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := a.compact(ctx); err != nil {
			return "", err
		}
		c.ui.Status("thinking…")
		msg, err := c.respond(ctx)
		if err != nil {
			return "", err
		}
		c.ui.Assistant(a.Steps, a.Cost, msg)
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}
		c.ui.Status(fmt.Sprintf("running %s…", plural(len(msg.ToolCalls), "tool")))
		outputs, err := c.executeRuns(ctx, msg.ToolCalls)
		if err != nil {
			return "", err
		}
		for i, call := range msg.ToolCalls {
			c.ui.Observation(call, outputs[i])
		}
	}
	if c.takeInterrupt() {
		return "", context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", ErrLimitsExceeded
}

// respond wraps Agent.respond with a cancelable context so Interrupt can stop
// an in-flight model call.
func (c *Chat) respond(ctx context.Context) (types.Message, error) {
	sctx, cancel := context.WithCancel(ctx)
	c.setStepCancel(cancel)
	msg, err := c.Agent.respond(sctx)
	c.setStepCancel(nil)
	cancel()
	if err != nil {
		if c.takeInterrupt() {
			return types.Message{}, context.Canceled
		}
		return types.Message{}, err
	}
	return msg, nil
}

// executeRuns wraps Agent.executeRuns with a cancelable context so Interrupt
// can stop long-running tools.
func (c *Chat) executeRuns(ctx context.Context, calls []types.ToolCall) ([]types.ExecutionOutput, error) {
	rctx, cancel := context.WithCancel(ctx)
	c.setStepCancel(cancel)
	outputs, err := c.Agent.executeRuns(rctx, calls)
	c.setStepCancel(nil)
	cancel()
	if err != nil {
		if c.takeInterrupt() {
			return outputs, context.Canceled
		}
		return outputs, err
	}
	return outputs, nil
}

func (c *Chat) takeInterrupt() bool {
	return c.interrupted.CompareAndSwap(true, false)
}

func (c *Chat) underBudget() bool {
	cfg := c.cfg
	if cfg.StepLimit > 0 && c.Steps >= cfg.StepLimit {
		return false
	}
	if cfg.CostLimit > 0 && c.Cost > cfg.CostLimit {
		return false
	}
	return true
}

// Close releases the session transcript writer.
func (c *Chat) Close() error {
	if c == nil || c.Agent == nil {
		return nil
	}
	return c.Agent.Close()
}
