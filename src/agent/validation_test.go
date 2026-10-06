package agent

import (
	"strings"
	"testing"

	"stubbs/src/config"
	"stubbs/src/types"
)

func toolAgent(names ...string) *Agent {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return &Agent{toolSet: set, toolNames: names, protocol: protocolNative}
}

func TestPartitionCalls(t *testing.T) {
	ia := &InteractiveAgent{Agent: toolAgent("bash", "file_read")}
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

func newTestSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	old := config.SessionsDir
	config.SessionsDir = dir
	t.Cleanup(func() { config.SessionsDir = old })
	s, err := newSession("test/model", "system")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRejectCallsNativePairsEachCall(t *testing.T) {
	s := newTestSession(t)
	ia := &InteractiveAgent{Agent: &Agent{Session: s, protocol: protocolNative, toolSet: map[string]bool{"bash": true}, toolNames: []string{"bash"}}}

	if err := ia.rejectCalls([]types.ToolCall{{ID: "1", Function: types.FunctionCall{Name: "read"}}}); err != nil {
		t.Fatal(err)
	}
	var toolMsgs []types.Message
	for _, m := range s.Messages {
		if m.Role == types.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 1 || toolMsgs[0].ToolCallID != "1" {
		t.Fatalf("tool responses = %+v", toolMsgs)
	}
	if !strings.Contains(toolMsgs[0].Content, "unknown tool") {
		t.Fatalf("response lacked guidance: %q", toolMsgs[0].Content)
	}
}

func TestRejectCallsTextUsesUserTurn(t *testing.T) {
	s := newTestSession(t)
	ia := &InteractiveAgent{Agent: &Agent{Session: s, protocol: protocolText, toolSet: map[string]bool{"bash": true}, toolNames: []string{"bash"}}}

	if err := ia.rejectCalls([]types.ToolCall{{ID: "1", Function: types.FunctionCall{Name: "read"}}}); err != nil {
		t.Fatal(err)
	}
	last := s.Messages[len(s.Messages)-1]
	if last.Role != types.RoleUser {
		t.Fatalf("text protocol should feed corrections as a user turn, got %q", last.Role)
	}
	if !strings.Contains(last.Content, "Unknown tool") {
		t.Fatalf("correction lacked guidance: %q", last.Content)
	}
}

func TestSystemPromptListsToolsAndGuidance(t *testing.T) {
	p := SystemPromptFor([]string{"file_edit", "bash"})
	for _, want := range []string{"bash", "file_edit", "never write tool calls as text", "do not rely on the sandbox /tmp path"} {
		if !strings.Contains(p, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, p)
		}
	}
}
