package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/types"
)

// fakeClient returns scripted responses in order.
type fakeClient struct {
	responses []llm.ORChatResponse
	calls     int
	lastMsgs  []types.Message
}

func (f *fakeClient) CompleteText(_ context.Context, messages []types.Message) (llm.ORChatResponse, error) {
	f.lastMsgs = messages
	if f.calls >= len(f.responses) {
		return llm.ORChatResponse{}, errors.New("no more scripted responses")
	}
	r := f.responses[f.calls]
	f.calls++
	return r, nil
}

func textResp(s string) llm.ORChatResponse {
	return llm.ORChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", Content: s}}}}
}

func toolResp(name, args string) llm.ORChatResponse {
	return llm.ORChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{
		Role: "assistant",
		ToolCalls: []types.ToolCall{{
			ID: "call-1", Type: "function",
			Function: types.FunctionCall{Name: name, Arguments: args},
		}},
	}}}}
}

type fakeTool struct {
	name  string
	calls int
}

func (t *fakeTool) Name() string                { return t.name }
func (t *fakeTool) Description() string         { return "fake" }
func (t *fakeTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *fakeTool) Execute(context.Context, string) types.ExecutionOutput {
	t.calls++
	return types.ExecutionOutput{Output: "tool output", Code: 0}
}

type fakeUI struct {
	assistants   int
	observations int
}

func (u *fakeUI) Status(string)                                     {}
func (u *fakeUI) Assistant(int, float32, types.Message)             { u.assistants++ }
func (u *fakeUI) Observation(types.ToolCall, types.ExecutionOutput) { u.observations++ }

func newTestChat(t *testing.T, client llm.ModelClient, registry *types.Registry) *Chat {
	t.Helper()
	environ := env.NewLocalEnvironment(env.EnvironmentConfig{}, registry)
	chat, err := NewChat(ChatConfig{StepLimit: 10}, client, environ, "test/model", &fakeUI{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Close() })
	return chat
}

func TestChatPlainReply(t *testing.T) {
	client := &fakeClient{responses: []llm.ORChatResponse{textResp("hello there")}}
	registry := types.NewRegistry()
	chat := newTestChat(t, client, registry)

	out, err := chat.Send(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello there" {
		t.Fatalf("out = %q", out)
	}
	if chat.Steps != 1 {
		t.Fatalf("Steps = %d, want 1", chat.Steps)
	}
}

func TestChatRunsToolThenReplies(t *testing.T) {
	tool := &fakeTool{name: "web"}
	registry := types.NewRegistry()
	registry.Register(tool)
	client := &fakeClient{responses: []llm.ORChatResponse{
		toolResp("web", `{"url":"https://example.com"}`),
		textResp("the answer is 42"),
	}}
	chat := newTestChat(t, client, registry)

	out, err := chat.Send(context.Background(), "look it up")
	if err != nil {
		t.Fatal(err)
	}
	if out != "the answer is 42" {
		t.Fatalf("out = %q", out)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
	if chat.Steps != 2 {
		t.Fatalf("Steps = %d, want 2", chat.Steps)
	}
}

func TestChatTurnStepsResetButCostAccumulates(t *testing.T) {
	client := &fakeClient{
		responses: []llm.ORChatResponse{
			{Choices: []llm.Choice{{Message: llm.ResponseMessage{Content: "one"}}}, Usage: &llm.Usage{Cost: 0.5}},
			{Choices: []llm.Choice{{Message: llm.ResponseMessage{Content: "two"}}}, Usage: &llm.Usage{Cost: 0.25}},
		},
	}
	chat := newTestChat(t, client, types.NewRegistry())

	if _, err := chat.Send(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if chat.Steps != 1 {
		t.Fatalf("after turn 1 Steps = %d, want 1", chat.Steps)
	}
	if _, err := chat.Send(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if chat.Steps != 1 {
		t.Fatalf("after turn 2 Steps = %d, want 1 (reset per turn)", chat.Steps)
	}
	if chat.Cost != 0.75 {
		t.Fatalf("Cost = %v, want 0.75", chat.Cost)
	}
}

func TestChatStepLimit(t *testing.T) {
	registry := types.NewRegistry()
	registry.Register(&fakeTool{name: "web"})
	client := &fakeClient{responses: []llm.ORChatResponse{
		toolResp("web", `{}`),
		toolResp("web", `{}`),
		toolResp("web", `{}`),
	}}
	environ := env.NewLocalEnvironment(env.EnvironmentConfig{}, registry)
	chat, err := NewChat(ChatConfig{StepLimit: 2}, client, environ, "test/model", &fakeUI{})
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()

	_, err = chat.Send(context.Background(), "go")
	if !errors.Is(err, ErrLimitsExceeded) {
		t.Fatalf("err = %v, want ErrLimitsExceeded", err)
	}
}

func TestChatInterrupt(t *testing.T) {
	client := &blockingClient{started: make(chan struct{})}
	chat := newTestChat(t, client, types.NewRegistry())

	go func() {
		<-client.started
		chat.Interrupt()
	}()

	_, err := chat.Send(context.Background(), "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// blockingClient blocks CompleteText until its context is canceled. It closes
// started once so the test knows the call is in flight.
type blockingClient struct {
	started   chan struct{}
	startOnce sync.Once
}

func (b *blockingClient) CompleteText(ctx context.Context, _ []types.Message) (llm.ORChatResponse, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-ctx.Done()
	return llm.ORChatResponse{}, ctx.Err()
}
