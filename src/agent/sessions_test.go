package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stubbs/src/config"
	"stubbs/src/types"
)

// writeRecords writes a session log named session_<id>.jsonl into dir.
func writeRecords(t *testing.T, dir, id string, records []sessionRecord) string {
	t.Helper()
	path := filepath.Join(dir, "session_"+id+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestListSessionsSortsNewestFirstAndCapturesFirstMessage(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	writeRecords(t, dir, "111", []sessionRecord{
		{Timestamp: old, Type: types.RoleUser, Content: "first task", Model: "m"},
		{Timestamp: old.Add(time.Minute), Type: types.RoleAssistant, Content: "reply", Model: "m"},
	})
	writeRecords(t, dir, "222", []sessionRecord{
		{Timestamp: newer, Type: types.RoleUser, Content: "newer task", Model: "m"},
	})

	got, err := listSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	if got[0].ID != "222" {
		t.Errorf("first entry ID = %s, want 222 (newest)", got[0].ID)
	}
	if got[0].FirstMessage != "newer task" {
		t.Errorf("first message = %q, want 'newer task'", got[0].FirstMessage)
	}
	if got[1].ID != "111" || got[1].FirstMessage != "first task" {
		t.Errorf("second entry = %+v", got[1])
	}
	if got[1].Messages != 2 {
		t.Errorf("message count = %d, want 2", got[1].Messages)
	}
	if !got[0].Updated.Equal(newer) {
		t.Errorf("updated = %v, want %v", got[0].Updated, newer)
	}
}

func TestListSessionsEmptyDir(t *testing.T) {
	got, err := listSessions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("empty dir should yield no sessions, got %+v", got)
	}
}

func TestListSessionsIgnoresCorruptRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_9.jsonl")
	content := `{"timestamp":"2024-01-01T00:00:00Z","type":"user","content":"hi","model":"m"}` + "\n" +
		`{not valid json` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := listSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FirstMessage != "hi" || got[0].Messages != 1 {
		t.Fatalf("corrupt line should be skipped, got %+v", got)
	}
}

func TestListSessionsFirstMessageSkipsAssistant(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, "1", []sessionRecord{
		{Timestamp: time.Now(), Type: types.RoleAssistant, Content: "welcome", Model: "m"},
		{Timestamp: time.Now(), Type: types.RoleUser, Content: "actual task", Model: "m"},
	})
	got, err := listSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FirstMessage != "actual task" {
		t.Fatalf("first user message = %+v, want 'actual task'", got)
	}
}

func TestLoadSessionRestoresContextAndAppends(t *testing.T) {
	dir := t.TempDir()
	orig := config.SessionsDir
	config.SessionsDir = dir
	t.Cleanup(func() { config.SessionsDir = orig })

	ts := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	writeRecords(t, dir, "12345", []sessionRecord{
		{Timestamp: ts, Type: types.RoleUser, Content: "hello", Model: "m"},
		{Timestamp: ts.Add(time.Second), Type: types.RoleAssistant, Content: "hi there", Model: "m"},
		{Timestamp: ts.Add(2 * time.Second), Type: types.RoleAssistant, Content: "", Model: "m"},
		{Timestamp: ts.Add(3 * time.Second), Type: types.RoleTool, Content: "tool out", ToolCallID: "x", Name: "bash", Model: "m"},
	})

	s, msgs, err := LoadSession("m2", "12345", SYSTEM_TEMPLATE)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.Id != 12345 {
		t.Errorf("id = %d, want 12345", s.Id)
	}
	if !s.CreatedAt.Equal(ts) {
		t.Errorf("CreatedAt = %v, want %v", s.CreatedAt, ts)
	}
	if len(msgs) != 5 { // system + 4 records
		t.Fatalf("got %d display messages, want 5", len(msgs))
	}
	if msgs[0].Role != types.RoleSystem {
		t.Errorf("first message role = %q, want system", msgs[0].Role)
	}
	if msgs[4].Name != "bash" || msgs[4].ToolCallID != "x" {
		t.Errorf("tool record not restored: %+v", msgs[4])
	}

	// Empty assistant text and tool results are kept for display but must not
	// enter the model context (they would be unpaired tool messages).
	ctx := s.ContextMessages(1 << 20)
	if len(ctx) != 3 { // system + user + non-empty assistant
		t.Fatalf("context has %d messages, want 3: %+v", len(ctx), ctx)
	}

	// Appending keeps writing to the resumed log.
	if err := s.Append(types.Message{Role: types.RoleUser, Content: "follow up"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "session_12345.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "follow up") {
		t.Error("append did not reach the resumed log")
	}
}

func TestLoadSessionMissingFile(t *testing.T) {
	dir := t.TempDir()
	orig := config.SessionsDir
	config.SessionsDir = dir
	t.Cleanup(func() { config.SessionsDir = orig })
	if _, _, err := LoadSession("m", "nope", SYSTEM_TEMPLATE); err == nil {
		t.Fatal("missing session should error")
	}
}

func TestLoadSessionBadID(t *testing.T) {
	dir := t.TempDir()
	orig := config.SessionsDir
	config.SessionsDir = dir
	t.Cleanup(func() { config.SessionsDir = orig })
	if err := os.WriteFile(filepath.Join(dir, "session_abc.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSession("m", "abc", SYSTEM_TEMPLATE); err == nil {
		t.Fatal("non-numeric id should error")
	}
}
