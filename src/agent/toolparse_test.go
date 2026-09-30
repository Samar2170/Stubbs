package agent

import (
	"encoding/json"
	"testing"
)

func TestParseAnthropicXMLToolCalls(t *testing.T) {
	content := `<function_calls>
<invoke name="file_edit">
<parameter name="file_path" string="true">src/x.go</parameter>
<parameter name="old_string" string="true">a</parameter>
<parameter name="new_string" string="true">b</parameter>
<parameter name="append" string="false">true</parameter>
</invoke>
</function_calls>`

	calls, stripped, ok := parseToolCalls(content)
	if !ok || len(calls) != 1 {
		t.Fatalf("calls = %+v, ok = %v", calls, ok)
	}
	if calls[0].Function.Name != "file_edit" {
		t.Fatalf("name = %q", calls[0].Function.Name)
	}
	var args struct {
		FilePath string `json:"file_path"`
		Old      string `json:"old_string"`
		Append   bool   `json:"append"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %q: %v", calls[0].Function.Arguments, err)
	}
	if args.FilePath != "src/x.go" || args.Old != "a" || !args.Append {
		t.Fatalf("args = %+v", args)
	}
	if stripped != "" {
		t.Fatalf("stripped = %q, want empty", stripped)
	}
}

func TestParseJSONToolCall(t *testing.T) {
	content := `Let me run it.
<tool_call>{"name":"bash","arguments":{"command":"ls -la"}}</tool_call>`

	calls, stripped, ok := parseToolCalls(content)
	if !ok || len(calls) != 1 {
		t.Fatalf("calls = %+v, ok = %v", calls, ok)
	}
	if calls[0].Function.Name != "bash" {
		t.Fatalf("name = %q", calls[0].Function.Name)
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if args.Command != "ls -la" {
		t.Fatalf("command = %q", args.Command)
	}
	if stripped != "Let me run it." {
		t.Fatalf("stripped = %q", stripped)
	}
}

func TestParseMultipleInvokes(t *testing.T) {
	content := `<function_calls>
<invoke name="file_read"><parameter name="file_path" string="true">a.go</parameter></invoke>
<invoke name="file_read"><parameter name="file_path" string="true">b.go</parameter></invoke>
</function_calls>`

	calls, _, ok := parseToolCalls(content)
	if !ok || len(calls) != 2 {
		t.Fatalf("calls = %+v, ok = %v", calls, ok)
	}
	if calls[0].ID == calls[1].ID {
		t.Fatalf("parsed calls must have unique ids: %+v", calls)
	}
}

func TestMalformedMarkupIsDetectedNotParsed(t *testing.T) {
	// The exact tail seen leaking into content in the wild.
	content := "Here is my plan.\n\nfile_path\" string=\"true\">src/tui/blocks.go"
	if !looksLikeToolMarkup(content) {
		t.Fatal("leaked parameter markup should be detected")
	}
	calls, _, ok := parseToolCalls(content)
	if ok || len(calls) != 0 {
		t.Fatalf("malformed fragment should not parse into calls: %+v", calls)
	}
}

func TestPlainTextIsNotMarkup(t *testing.T) {
	if looksLikeToolMarkup("I edited src/tui/blocks.go and ran the tests.") {
		t.Fatal("plain prose should not look like tool markup")
	}
}
