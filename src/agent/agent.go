package agent

import (
	"context"
	"fmt"
	"strings"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/types"
	"time"
)

var SYSTEM_TEMPLATE = "You are a helpful assistant that can interact with a computer."

type AgentConfig struct {
	StepLimit     int
	CostLimit     float32
	WallTimeLimit int
}

type Agent struct {
	config      *AgentConfig
	Model       string
	Tools       []types.Tool
	ModelClient llm.ModelClient
	StartTime   time.Time
	Cost        float32
	Steps       int
	ModelCalls  int
	Messages    []types.Message
	Environment env.Environment
	Session     *Session
}

func NewAgent(cfg *AgentConfig, client llm.ModelClient, environ env.Environment, model string) (*Agent, error) {
	if cfg == nil {
		cfg = &AgentConfig{}
	}
	if client == nil {
		return nil, fmt.Errorf("agent: client cannot be nil")
	}
	s, err := newSession(model, SYSTEM_TEMPLATE)
	if err != nil {
		return nil, err
	}
	return &Agent{
		config:      cfg,
		Model:       model,
		ModelClient: client,
		Environment: environ,
		StartTime:   time.Now(),
		Session:     s,
		Messages:    []types.Message{{Role: types.RoleSystem, Content: SYSTEM_TEMPLATE}},
	}, nil
}

func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	defer a.Close()
	if a.config.WallTimeLimit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.config.WallTimeLimit)*time.Second)
		defer cancel()
	}
	if err := a.appendMessage(types.Message{Role: "user", Content: task}); err != nil {
		return "", err
	}
	var resp string
	for a.Steps < a.config.StepLimit && (a.config.CostLimit <= 0 || a.Cost <= a.config.CostLimit) {
		content, err := a.step(ctx)
		if err != nil {
			return "", err
		}
		if content != "" {
			resp = content
			break
		}
	}
	return resp, nil
}

func (a *Agent) appendMessage(msg types.Message) error {
	if err := a.Session.Append(msg); err != nil {
		return fmt.Errorf("append to session: %w", err)
	}
	a.Messages = append(a.Messages, msg)
	return nil
}

func (a *Agent) query(ctx context.Context) (llm.ORChatResponse, error) {
	resp, err := a.ModelClient.CompleteText(ctx, a.getMessages())
	if err != nil {
		return llm.ORChatResponse{}, err
	}
	a.ModelCalls += 1
	if resp.Usage != nil {
		a.Cost += resp.Usage.Cost
	}
	return resp, nil
}

func (a *Agent) getMessages() []types.Message {
	return a.Messages
}

func (a *Agent) step(ctx context.Context) (string, error) {
	resp, err := a.query(ctx)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("agent: model response has no choices")
	}
	choice := resp.Choices[0]
	assistant := types.Message{
		Role:      types.RoleAssistant,
		Content:   choice.Message.Content,
		ToolCalls: choice.Message.ToolCalls,
	}
	if err := a.appendMessage(assistant); err != nil {
		return "", err
	}
	if len(assistant.ToolCalls) == 0 {
		return assistant.Content, nil
	}
	for _, call := range assistant.ToolCalls {
		out := a.Environment.Execute(ctx, call)
		if err := a.appendMessage(types.Message{
			Role:       types.RoleTool,
			Content:    renderExecution(out),
			ToolCallID: call.ID,
			Name:       call.Function.Name,
		}); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (a *Agent) prepareMessage(message types.Message) []types.Message {
	if err := a.Session.Append(message); err != nil {
		return nil
	}
	return a.Session.History()
}

func renderExecution(out types.ExecutionOutput) string {
	var b strings.Builder
	b.WriteString(out.Output)
	if out.Error != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(out.Error)
	}
	if out.Code != 0 {
		fmt.Fprintf(&b, "\n[exit code: %d]", out.Code)
	}
	return b.String()
}

func (a *Agent) Close() error {
	if a == nil || a.Session == nil {
		return nil
	}
	return a.Session.Close()
}
