package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseTextToolCallSingle(t *testing.T) {
	content := "Running it now.\n\n```stubbs-tool\n{\"name\": \"bash\", \"arguments\": {\"command\": \"ls -la\"}}\n```"

	calls, stripped, saw := parseTextToolCalls(content)
	if !saw || len(calls) != 1 {
		t.Fatalf("calls = %+v, saw = %v", calls, saw)
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
	if stripped != "Running it now." {
		t.Fatalf("stripped = %q", stripped)
	}
}

func TestParseTextToolCallArray(t *testing.T) {
	content := "```stubbs-tool\n[{\"name\":\"file_read\",\"arguments\":{\"file_path\":\"a.go\"}},{\"name\":\"file_read\",\"arguments\":{\"file_path\":\"b.go\"}}]\n```"

	calls, _, saw := parseTextToolCalls(content)
	if !saw || len(calls) != 2 {
		t.Fatalf("calls = %+v, saw = %v", calls, saw)
	}
	if calls[0].ID == calls[1].ID {
		t.Fatalf("ids must be unique: %+v", calls)
	}
}

func TestParseTextToolCallArgumentsAsString(t *testing.T) {
	content := "```stubbs-tool\n{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}\n```"
	calls, _, _ := parseTextToolCalls(content)
	if len(calls) != 1 || calls[0].Function.Arguments != `{"command":"pwd"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestMalformedTextBlockDetected(t *testing.T) {
	content := "```stubbs-tool\n{not json}\n```"
	calls, _, saw := parseTextToolCalls(content)
	if !saw {
		t.Fatal("a stubbs-tool block should be detected even when malformed")
	}
	if len(calls) != 0 {
		t.Fatalf("malformed block should not produce calls: %+v", calls)
	}
}

func TestLooksLikeToolMarkup(t *testing.T) {
	markup := []string{
		`<invoke name="bash">`,
		`Here is my plan.\n\nfile_path" string="true">src/tui/blocks.go`,
	}
	for _, s := range markup {
		if !looksLikeToolMarkup(s) {
			t.Fatalf("expected markup: %q", s)
		}
	}
	if looksLikeToolMarkup("I edited src/tui/blocks.go and ran the tests.") {
		t.Fatal("plain prose should not look like markup")
	}
}

func TestTextToolInstructionListsTools(t *testing.T) {
	got := textToolInstruction([]string{"bash", "file_edit"})
	for _, want := range []string{"stubbs-tool", "bash", "file_edit", "Never emit XML"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instruction missing %q:\n%s", want, got)
		}
	}
}
