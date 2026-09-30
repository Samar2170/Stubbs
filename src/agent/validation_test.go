package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"stubbs/src/config"
	"stubbs/src/types"
)

type fakeEnv struct{ names []string }

func (f fakeEnv) Execute(context.Context, types.ToolCall) types.ExecutionOutput {
	return types.ExecutionOutput{}
}

func (f fakeEnv) Has(name string) bool {
	for _, n := range f.names {
		if n == name {
			return true
		}
	}
	return false
}

func (f fakeEnv) ToolNames() []string { return f.names }

func TestPartitionCalls(t *testing.T) {
	ia := &InteractiveAgent{Agent: &Agent{Environment: fakeEnv{names: []string{"bash", "file_read"}}}}
	known, unknown := ia.partitionCalls([]types.ToolCall{
		{Function: types.FunctionCall{Name: "bash"}},
		{Function: types.FunctionCall{Name: "read"}},
		{Function: types.FunctionCall{Name: "file_read"}},
	})
	if len(known) != 2 {
		t.Fatalf("known = %+v, want 2", known)
	}
	if len(unknown) != 1 || unknown[0].Function.Name != "read" {
		t.Fatalf("unknown = %+v, want [read]", unknown)
	}
}

func TestRejectCallsAnswersEveryCall(t *testing.T) {
	dir := t.TempDir()
	old := config.SessionsDir
	config.SessionsDir = dir
	defer func() { config.SessionsDir = old }()

	s, err := newSession("test/model", "system")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ia := &InteractiveAgent{Agent: &Agent{Environment: fakeEnv{names: []string{"bash"}}, Session: s}}
	calls := []types.ToolCall{
		{ID: "1", Function: types.FunctionCall{Name: "read"}},
		{ID: "2", Function: types.FunctionCall{Name: "bash"}},
	}
	if err := ia.rejectCalls(calls); err != nil {
		t.Fatal(err)
	}
	var toolMsgs []types.Message
	for _, m := range s.Messages {
		if m.Role == types.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 2 {
		t.Fatalf("got %d tool responses, want 2", len(toolMsgs))
	}
	if toolMsgs[0].ToolCallID != "1" {
		t.Fatalf("first response = %+v", toolMsgs[0])
	}
	if !strings.Contains(toolMsgs[0].Content, "unknown tool") {
		t.Fatalf("unknown tool response lacked guidance: %q", toolMsgs[0].Content)
	}
	if !strings.Contains(toolMsgs[1].Content, "not executed") {
		t.Fatalf("known tool response should be marked not executed: %q", toolMsgs[1].Content)
	}
}

type promptTool struct{ name string }

func (p promptTool) Name() string                { return p.name }
func (p promptTool) Description() string         { return "" }
func (p promptTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (p promptTool) Execute(context.Context, string) types.ExecutionOutput {
	return types.ExecutionOutput{}
}

func TestSystemPromptListsToolsAndGuidance(t *testing.T) {
	p := SystemPromptFor([]types.Tool{promptTool{"file_edit"}, promptTool{"bash"}})
	for _, want := range []string{"bash", "file_edit", "never write tool calls as text", "do not rely on /tmp"} {
		if !strings.Contains(p, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, p)
		}
	}
}
