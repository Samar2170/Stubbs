package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/memory"
	"stubbs/src/types"
	"sync"
	"time"
)

var SYSTEM_TEMPLATE = "You are a helpful assistant that can interact with a computer."

// maxMalformedRetries bounds how many consecutive turns the agent will spend
// correcting unparseable or tool-less model output before giving up.
const maxMalformedRetries = 3

// toolProtocol selects how the model exchanges tool calls with the harness.
type toolProtocol int

const (
	protocolNative toolProtocol = iota // provider-native tool_calls
	protocolText                       // fenced stubbs-tool JSON in content
)

// errRetryTurn signals that a turn should be retried immediately without being
// recorded as a final answer (used when downgrading the tool protocol).
var errRetryTurn = errors.New("agent: retry turn")

// SystemPromptFor builds the system prompt, listing the tools that are actually
// registered so the model does not invent names, and forbidding the XML
// tool-call style some providers emit instead of native function calls.
func SystemPromptFor(names []string) string {
	names = sortedUnique(names)

	var b strings.Builder
	b.WriteString(SYSTEM_TEMPLATE)
	b.WriteString("\n\nUse the provided function-calling tools to act on the computer; never write tool calls as text, XML, or JSON in your reply.\n")
	if len(names) > 0 {
		fmt.Fprintf(&b, "Available tools: %s.\n", strings.Join(names, ", "))
	}
	b.WriteString("Only call the tools listed above, with exactly those names. If you need a capability that no tool provides, say so in plain text instead of inventing a tool.")
	b.WriteString("\nPut throwaway scripts and notes in $TMPDIR or a scratch directory under $HOME; do not add temporary *_test.go files to a Go package, and do not rely on the sandbox /tmp path.")
	return b.String()
}

func sortedUnique(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// toolMarkupCorrection is fed back to the model when its reply contained
// tool-call markup the harness could not parse.
const toolMarkupCorrection = "Your previous reply contained tool-call markup that could not be parsed. Use the provided tools with exactly the listed names and the required format."

type AgentConfig struct {
	StepLimit     int
	CostLimit     float32
	WallTimeLimit int
	WorkingDir    string
	SystemPrompt  string
	ToolNames     []string
	Memory        *memory.Store
}

type Agent struct {
	config      *AgentConfig
	model       string
	modelMu     sync.RWMutex
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
	protocol    toolProtocol
	toolNames   []string
	toolSet     map[string]bool
}

func NewAgent(cfg *AgentConfig, client llm.ModelClient, environ env.Environment, model string, contextEnabled bool) (*Agent, error) {
	if cfg == nil {
		cfg = &AgentConfig{}
	}
	if client == nil {
		return nil, fmt.Errorf("agent: client cannot be nil")
	}
	sysPrompt := cfg.SystemPrompt
	if strings.TrimSpace(sysPrompt) == "" {
		sysPrompt = SYSTEM_TEMPLATE
	}
	s, err := newSession(model, sysPrompt)
	if err != nil {
		return nil, err
	}
	names := sortedUnique(cfg.ToolNames)
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return &Agent{
		config:      cfg,
		model:       model,
		ModelClient: client,
		Environment: environ,
		StartTime:   time.Now(),
		Session:     s,
		Memory:      cfg.Memory,
		WorkingDir:  cfg.WorkingDir,
		protocol:    protocolNative,
		toolNames:   names,
		toolSet:     set,
	}, nil
}

// hasTool reports whether name is one of the registered tools.
func (a *Agent) hasTool(name string) bool { return a.toolSet[name] }

// toolNameList returns the registered tool names.
func (a *Agent) toolNameList() []string { return a.toolNames }

// setProtocol switches the tool transport, keeping the client in step and
// telling the model about the text protocol when downgrading.
func (a *Agent) setProtocol(p toolProtocol) {
	if a.protocol == p {
		return
	}
	a.protocol = p
	if sw, ok := a.ModelClient.(interface{ SetNativeTools(bool) }); ok {
		sw.SetNativeTools(p == protocolNative)
	}
	if p == protocolText {
		_ = a.appendMessage(types.Message{Role: types.RoleUser, Content: textToolInstruction(a.toolNames)})
	}
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
	var resp string
	malformed := 0
	for a.Steps < a.config.StepLimit && (a.config.CostLimit <= 0 || a.Cost <= a.config.CostLimit) {
		content, corrective, err := a.step(ctx)
		if err != nil {
			return "", err
		}
		if corrective {
			malformed++
			if malformed > maxMalformedRetries {
				return resp, nil
			}
			if err := a.rejectMalformed(content); err != nil {
				return "", err
			}
			continue
		}
		if content == "" {
			continue
		}
		resp = content
		break
	}
	return resp, nil
}

// ResumeSession swaps the agent's active session for a previously persisted
// one (see LoadSession) and closes the session it replaces. Subsequent messages
// are appended to the resumed log, so a run continues the old conversation.
func (a *Agent) ResumeSession(s *Session) {
	if a == nil || s == nil {
		return
	}
	if a.Session != nil && a.Session != s {
		_ = a.Session.Close()
	}
	a.Session = s
}

// ModelName returns the active model. Safe to call concurrently with SetModel.
func (a *Agent) ModelName() string {
	if a == nil {
		return ""
	}
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.model
}

// SetModel switches the model used for subsequent requests and records the
// change on the session. It is a no-op when the model is already active.
func (a *Agent) SetModel(model string) {
	if a == nil || model == "" {
		return
	}
	a.modelMu.Lock()
	changed := a.model != model
	if changed {
		a.model = model
	}
	a.modelMu.Unlock()
	if !changed {
		return
	}
	if sw, ok := a.ModelClient.(interface{ SetModel(string) }); ok {
		sw.SetModel(model)
	}
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
	if sw, ok := a.ModelClient.(interface{ SetNativeTools(bool) }); ok {
		sw.SetNativeTools(a.protocol == protocolNative)
	}
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

// modelResponse is one model turn: the assistant message plus any tool calls
// recovered from the text protocol.
type modelResponse struct {
	msg         types.Message
	textCalls   []types.ToolCall
	parseFailed bool // tool markup was present but yielded no calls
}

func (a *Agent) respond(ctx context.Context) (modelResponse, error) {
	a.Steps++
	resp, err := a.query(ctx)
	if err != nil {
		_ = a.Session.AppendError("model call", err)
		return modelResponse{}, err
	}
	if len(resp.Choices) == 0 {
		return modelResponse{}, fmt.Errorf("agent: model response has no choices")
	}
	choice := resp.Choices[0]
	msg := types.Message{
		Role:      types.RoleAssistant,
		Content:   choice.Message.Content,
		ToolCalls: choice.Message.ToolCalls,
	}

	if a.protocol == protocolNative {
		// The model answered with tool-call markup instead of native
		// tool_calls. Do not fabricate calls from it: switch the session to the
		// text protocol and retry the turn.
		if len(msg.ToolCalls) == 0 && looksLikeToolMarkup(msg.Content) {
			_ = a.Session.AppendError("tool protocol downgrade", fmt.Errorf("%s", truncate(msg.Content, 200)))
			a.setProtocol(protocolText)
			return modelResponse{}, errRetryTurn
		}
		if err := a.appendMessage(msg); err != nil {
			return modelResponse{}, err
		}
		return modelResponse{msg: msg}, nil
	}

	// Text protocol: recover calls from the documented fenced block.
	calls, stripped, sawMarkup := parseTextToolCalls(msg.Content)
	if sawMarkup {
		msg.Content = stripped
	}
	if err := a.appendMessage(msg); err != nil {
		return modelResponse{}, err
	}
	failed := len(calls) == 0 && (sawMarkup || looksLikeToolMarkup(msg.Content))
	return modelResponse{msg: msg, textCalls: calls, parseFailed: failed}, nil
}

// appendToolResult records a tool execution in the session log with its
// structured outcome and appends the matching tool message to the history.
func (a *Agent) appendToolResult(call types.ToolCall, out types.ExecutionOutput) error {
	if err := a.Session.AppendToolResult(call, out); err != nil {
		return fmt.Errorf("append tool result: %w", err)
	}
	return nil
}

// rejectMalformed logs an unparseable assistant reply and feeds a corrective
// user turn back so the model can retry with native tool calls.
func (a *Agent) rejectMalformed(content string) error {
	_ = a.Session.AppendError("malformed assistant output", fmt.Errorf("%s", truncate(content, 200)))
	return a.appendMessage(types.Message{Role: types.RoleUser, Content: toolMarkupCorrection})
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (a *Agent) executeRuns(ctx context.Context, calls []types.ToolCall) ([]types.ExecutionOutput, error) {
	outputs := make([]types.ExecutionOutput, 0, len(calls))
	var text strings.Builder
	for _, call := range calls {
		out := a.Environment.Execute(ctx, call)
		a.captureHeuristic(call, out)
		outputs = append(outputs, out)
		if a.protocol == protocolText {
			// No native pairing in the text protocol: log the result and feed
			// it back as a user turn.
			_ = a.Session.AppendToolEvent(call, out)
			fmt.Fprintf(&text, "%s(%s)\n%s\n\n", call.Function.Name, call.Function.Arguments, renderExecution(out))
			continue
		}
		if err := a.appendToolResult(call, out); err != nil {
			return outputs, err
		}
	}
	if a.protocol == protocolText && len(calls) > 0 {
		if err := a.appendMessage(types.Message{Role: types.RoleUser, Content: "Tool results:\n\n" + text.String()}); err != nil {
			return outputs, err
		}
	}
	return outputs, nil
}

// step runs one model turn. corrective is true when the reply must be retried
// (the caller feeds a correction back); content carries the reply so the caller
// can log it.
func (a *Agent) step(ctx context.Context) (content string, corrective bool, err error) {
	mr, err := a.respond(ctx)
	if errors.Is(err, errRetryTurn) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	calls := mr.msg.ToolCalls
	if a.protocol == protocolText {
		calls = mr.textCalls
	}
	if len(calls) == 0 {
		return mr.msg.Content, mr.parseFailed || looksLikeToolMarkup(mr.msg.Content), nil
	}
	if _, err := a.executeRuns(ctx, calls); err != nil {
		return "", false, err
	}
	return "", false, nil
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
