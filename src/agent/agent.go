package agent

import (
	"context"
	"fmt"
	"strings"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/memory"
	"stubbs/src/types"
	"time"
)

var SYSTEM_TEMPLATE = "You are a helpful assistant that can interact with a computer."

type AgentConfig struct {
	StepLimit     int
	CostLimit     float32
	WallTimeLimit int
	WorkingDir    string
	Memory        *memory.Store
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
	Environment env.Environment
	Session     *Session
	Memory      *memory.Store
	WorkingDir  string
}

func NewAgent(cfg *AgentConfig, client llm.ModelClient, environ env.Environment, model string, contextEnabled bool) (*Agent, error) {
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
		Memory:      cfg.Memory,
		WorkingDir:  cfg.WorkingDir,
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
	a.ensureRepoMap(ctx)
	a.injectMemory(task)
	defer a.summarizeMemory(ctx)
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

// SetModel switches the model used for subsequent requests and records the
// change on the session. It is a no-op when the model is already active.
func (a *Agent) SetModel(model string) {
	if a == nil || model == "" || a.Model == model {
		return
	}
	if sw, ok := a.ModelClient.(interface{ SetModel(string) }); ok {
		sw.SetModel(model)
	}
	a.Model = model
	if a.Session != nil {
		a.Session.SetModel(model)
	}
}

func (a *Agent) appendMessage(msg types.Message) error {
	if err := a.Session.Append(msg); err != nil {
		return fmt.Errorf("append to session: %w", err)
	}
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
	return a.Session.ContextMessages(contextBudget)
}

func (a *Agent) injectMemory(task string) {
	if a.Memory == nil || a.Session == nil {
		return
	}
	text := a.memoryPreamble(task)
	if text == "" {
		return
	}
	a.Session.InjectMemory(text, 1)
}

func (a *Agent) memoryPreamble(task string) string {
	if a.Memory == nil {
		return ""
	}
	opts := a.Memory.Options()
	budget := opts.BudgetTokens
	var sections []string
	total := 0
	add := func(text string) {
		tokens := estimateTokens(text)
		if budget > 0 && total+tokens > budget {
			return
		}
		sections = append(sections, text)
		total += tokens
	}

	core, err := a.Memory.Core()
	if err == nil {
		for _, e := range core {
			if strings.TrimSpace(e.Body) == "" {
				continue
			}
			add(fmt.Sprintf("## %s\n%s", e.Title, e.Body))
		}
	}

	if repoMap, err := a.Memory.RepoMap(); err == nil && repoMap != "" {
		add("## Repository map\n" + repoMap)
	}

	topK := opts.TopK
	if topK <= 0 {
		topK = 5
	}
	if relevant, err := a.Memory.Search(task, topK); err == nil {
		for _, e := range relevant {
			add(fmt.Sprintf("- [%s] %s: %s", e.Kind, e.Title, e.Body))
		}
	}

	if len(sections) == 0 {
		return ""
	}
	return "Persistent project memory (may be stale; verify against the repository):\n\n" +
		strings.Join(sections, "\n\n")
}

func (a *Agent) respond(ctx context.Context) (types.Message, error) {
	a.Steps++
	resp, err := a.query(ctx)
	if err != nil {
		return types.Message{}, err
	}
	if len(resp.Choices) == 0 {
		return types.Message{}, fmt.Errorf("agent: model response has no choices")
	}
	choice := resp.Choices[0]
	msg := types.Message{
		Role:      types.RoleAssistant,
		Content:   choice.Message.Content,
		ToolCalls: choice.Message.ToolCalls,
	}
	if err := a.appendMessage(msg); err != nil {
		return types.Message{}, err
	}
	return msg, nil
}

func (a *Agent) executeRuns(ctx context.Context, calls []types.ToolCall) ([]types.ExecutionOutput, error) {
	outputs := make([]types.ExecutionOutput, 0, len(calls))
	for _, call := range calls {
		out := a.Environment.Execute(ctx, call)
		a.captureHeuristic(call, out)
		outputs = append(outputs, out)
		if err := a.appendMessage(types.Message{
			Role:       types.RoleTool,
			Content:    renderExecution(out),
			ToolCallID: call.ID,
			Name:       call.Function.Name,
		}); err != nil {
			return outputs, err
		}
	}
	return outputs, nil
}

func (a *Agent) step(ctx context.Context) (string, error) {
	msg, err := a.respond(ctx)
	if err != nil {
		return "", err
	}
	if len(msg.ToolCalls) == 0 {
		return msg.Content, nil
	}
	if _, err := a.executeRuns(ctx, msg.ToolCalls); err != nil {
		return "", err
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
