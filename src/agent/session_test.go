package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stubbs/src/config"
	"stubbs/src/types"
)

func readRecords(t *testing.T, path string) []sessionRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	var out []sessionRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var rec sessionRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestSessionLogsStructuredToolCalls(t *testing.T) {
	dir := t.TempDir()
	old := config.SessionsDir
	config.SessionsDir = dir
	defer func() { config.SessionsDir = old }()

	s, err := newSession("test/model", "system")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	call := types.ToolCall{
		ID:       "call_1",
		Type:     "function",
		Function: types.FunctionCall{Name: "file_edit", Arguments: `{"file_path":"a.go"}`},
	}
	if err := s.Append(types.Message{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{call}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendToolResult(call, types.ExecutionOutput{Output: "ok", Duration: 12 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendError("model call", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendError("ignored", nil); err != nil {
		t.Fatal(err)
	}

	recs := readRecords(t, filepath.Join(dir, fmt.Sprintf("session_%d.jsonl", s.Id)))
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3: %+v", len(recs), recs)
	}

	asst := recs[0]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Function.Name != "file_edit" {
		t.Fatalf("assistant tool calls not logged: %+v", asst)
	}
	if asst.ToolCalls[0].Function.Arguments != `{"file_path":"a.go"}` {
		t.Fatalf("arguments not logged: %q", asst.ToolCalls[0].Function.Arguments)
	}

	tool := recs[1]
	if tool.Type != types.RoleTool || tool.Name != "file_edit" || tool.ToolCallID != "call_1" {
		t.Fatalf("tool record missing structured fields: %+v", tool)
	}
	if tool.DurationMS != 12 {
		t.Fatalf("duration_ms = %d, want 12", tool.DurationMS)
	}
	if tool.Error != "" || tool.Code != 0 {
		t.Fatalf("unexpected error/code on success: %+v", tool)
	}

	evt := recs[2]
	if evt.Type != "error" || !strings.Contains(evt.Content, "model call: boom") {
		t.Fatalf("error event not logged: %+v", evt)
	}
}

func TestSessionLogsToolError(t *testing.T) {
	dir := t.TempDir()
	old := config.SessionsDir
	config.SessionsDir = dir
	defer func() { config.SessionsDir = old }()

	s, err := newSession("test/model", "system")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	call := types.ToolCall{ID: "c", Function: types.FunctionCall{Name: "bash", Arguments: "{}"}}
	if err := s.AppendToolResult(call, types.ExecutionOutput{Error: "boom", Code: -1}); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, filepath.Join(dir, fmt.Sprintf("session_%d.jsonl", s.Id)))
	if len(recs) != 1 || recs[0].Error != "boom" || recs[0].Code != -1 {
		t.Fatalf("tool error fields missing: %+v", recs)
	}
}
