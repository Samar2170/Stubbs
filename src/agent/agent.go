package agent

import (
	"context"
	"fmt"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/types"
	"time"
)

var SYSTEM_TEMPLATE = "You are a helpful assistant that can interact with a computer."

type AgentConfig struct {
	InstanceTemplate           string
	StepLimit                  int
	CostLimit                  float32
	WalltimeLimit              int
	MaxConsecutiveFormatErrors int
}

type Agent struct {
	config      *AgentConfig
	Model       string
	maxTokens   int
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

func NewAgent(config *AgentConfig, tools []types.Tool, client llm.ModelClient, environ env.Environment, model string) *Agent {
	return &Agent{
		config: config,
		Model:  model,
		// maxTokens:   maxTokens,
		Tools:       tools,
		ModelClient: client,
		StartTime:   time.Now(),
		Cost:        0,
		Steps:       0,
		Messages:    []types.Message{},
		Environment: environ,
		Session:     newSession(model, SYSTEM_TEMPLATE),
	}
}

func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	var resp string
	a.Messages = append(a.Messages, types.Message{Role: "user", Content: task})
	if err := a.Session.Append(types.Message{Role: "user", Content: task}); err != nil {
		return resp, err
	}
	for a.Steps < a.config.StepLimit {
		err := a.step(ctx)
		if err != nil {
			return "", err
		}
		if a.Messages[len(a.Messages)-1].Role == "exit" {
			break
		}
	}
	a.Session.Close()
	return "", nil
}

func (a *Agent) appendMessage(msg types.Message) {
	a.Messages = append(a.Messages, msg)
	a.Session.Append(msg)
}

func (a *Agent) query(ctx context.Context) (llm.ORChatResponse, error) {
	resp, err := a.ModelClient.CompleteText(ctx, a.getMessages())
	if err != nil {
		return llm.ORChatResponse{}, err
	}
	a.ModelCalls += 1
	a.Cost += resp.Usage.Cost
	return resp, nil
}

func (a *Agent) getMessages() []types.Message {
	return a.Messages
}

func (a *Agent) step(ctx context.Context) error {
	resp, err := a.query(ctx)
	if err != nil {
		return err
	}
	if len(resp.Choices) == 0 {
		return fmt.Errorf("agent: model response has no choices")
	}
	a.appendMessage(
		types.Message{
			Role:    types.RoleAssistant,
			Content: resp.Choices[0].Message.Content,
		},
	)
	outputs := []string{}
	for _, choice := range resp.Choices {
		if len(choice.Message.ToolCalls) > 0 {
			for _, call := range choice.Message.ToolCalls {
				excOutput := a.Environment.Execute(call, ".", 500)
				if excOutput.Error != "" {
					outputs = append(outputs, excOutput.Error)
				} else {
					outputs = append(outputs, excOutput.Output)
				}
			}
		}
	}
	fmt.Println(outputs)

	for _, op := range outputs {
		a.appendMessage(
			types.Message{
				Role:    types.RoleTool,
				Content: op,
			},
		)
	}
	return nil
}

func (a *Agent) prepareMessage(message types.Message) []types.Message {
	if err := a.Session.Append(message); err != nil {
		return nil
	}
	return a.Session.History()
}

func (a *Agent) Close() error {
	if a == nil || a.Session == nil {
		return nil
	}
	return a.Session.Close()
}
