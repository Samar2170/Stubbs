package llm

import (
	"strings"
	"testing"
)

func TestDecodeStreamAssemblesContentAndToolCalls(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"ba","arguments":"{\"command\":\"l"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"sh","arguments":"s\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
		`data: [DONE]`,
	}, "\n")

	resp, err := decodeStream(strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("got %d choices", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if msg.Content != "Hello" {
		t.Fatalf("content = %q, want Hello", msg.Content)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "bash" {
		t.Fatalf("tool call = %+v", tc)
	}
	if tc.Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("arguments = %q", tc.Function.Arguments)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}
