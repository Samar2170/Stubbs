package agent

import (
	"context"
	"strings"
	"testing"

	"stubbs/src/llm"
	"stubbs/src/memory"
	"stubbs/src/types"
)

func TestParseMemoryEntries(t *testing.T) {
	raw := "```json\n" +
		`[{"kind":"preference","title":"Tabs","body":"Use tabs","tags":["style"],"importance":0.9},` +
		`{"kind":"bogus","title":"X","body":"Y"}]` +
		"\n```"
	got := parseMemoryEntries(raw)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Kind != memory.KindPreference || got[0].Title != "Tabs" || got[0].Importance != 0.9 {
		t.Fatalf("first entry = %+v", got[0])
	}
	if got[1].Kind != memory.KindProjectFact {
		t.Fatalf("unknown kind should fall back to project-fact, got %q", got[1].Kind)
	}
}

func TestParseMemoryEntriesInvalid(t *testing.T) {
	if got := parseMemoryEntries("not json at all"); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
	if got := parseMemoryEntries("[]"); len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}

func TestRedactSecrets(t *testing.T) {
	in := "OPENROUTER_API_KEY=sk-abc123\nnormal line\nPASSWORD: hunter2\nno secret here"
	got := redactSecrets(in)
	if strings.Contains(got, "sk-abc123") || strings.Contains(got, "hunter2") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "normal line") || !strings.Contains(got, "no secret here") {
		t.Fatalf("non-secret lines removed: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction marker: %q", got)
	}
}

type fakeCompleter struct {
	content string
	called  bool
}

func (f *fakeCompleter) Complete(ctx context.Context, msgs []types.Message) (llm.ORChatResponse, error) {
	f.called = true
	return llm.ORChatResponse{Choices: []llm.Choice{{
		Message: llm.ResponseMessage{Content: f.content},
	}}}, nil
}

func (f *fakeCompleter) CompleteText(ctx context.Context, msgs []types.Message) (llm.ORChatResponse, error) {
	return f.Complete(ctx, msgs)
}

func TestSummarizeMemoryAddsEntries(t *testing.T) {
	store, err := memory.NewStore(memory.Options{Dir: t.TempDir(), AutoSummarize: true})
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCompleter{content: `[{"kind":"project-fact","title":"Tests","body":"Run go test ./src/...","importance":0.8}]`}
	a := &Agent{
		Memory:      store,
		ModelClient: fc,
		Session: &Session{Messages: []types.Message{
			{Role: types.RoleUser, Content: strings.Repeat("please implement the feature. ", 40)},
			{Role: types.RoleAssistant, Content: strings.Repeat("done, tests pass. ", 40)},
		}},
	}
	a.summarizeMemory(context.Background())
	if !fc.called {
		t.Fatal("completer was not called")
	}
	entries, _ := store.Load()
	if len(entries) != 1 || entries[0].Title != "Tests" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestSummarizeMemoryDisabled(t *testing.T) {
	store, err := memory.NewStore(memory.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCompleter{content: "[]"}
	a := &Agent{
		Memory:      store,
		ModelClient: fc,
		Session: &Session{Messages: []types.Message{
			{Role: types.RoleUser, Content: strings.Repeat("x ", 500)},
		}},
	}
	a.summarizeMemory(context.Background())
	if fc.called {
		t.Fatal("model should not be called when auto-summarize is disabled")
	}
}

func TestSummarizeMemorySkipsTinySessions(t *testing.T) {
	store, err := memory.NewStore(memory.Options{Dir: t.TempDir(), AutoSummarize: true})
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCompleter{content: "[]"}
	a := &Agent{
		Memory:      store,
		ModelClient: fc,
		Session:     &Session{Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}}},
	}
	a.summarizeMemory(context.Background())
	if fc.called {
		t.Fatal("tiny sessions should not trigger summarization")
	}
}

func TestCaptureHeuristicDedupes(t *testing.T) {
	store, err := memory.NewStore(memory.Options{Dir: t.TempDir(), CaptureHeuristics: true})
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Memory: store}
	call := types.ToolCall{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"go test ./src/..."}`}}
	out := types.ExecutionOutput{Code: 0}
	a.captureHeuristic(call, out)
	a.captureHeuristic(call, out)
	entries, _ := store.Load()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (deduped)", len(entries))
	}
	if entries[0].Kind != memory.KindProcedure || !strings.Contains(entries[0].Body, "go test") {
		t.Fatalf("entry = %+v", entries[0])
	}
}

func TestCaptureHeuristicIgnoresFailuresAndNonBuilds(t *testing.T) {
	store, err := memory.NewStore(memory.Options{Dir: t.TempDir(), CaptureHeuristics: true})
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Memory: store}
	failed := types.ToolCall{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"go test ./..."}`}}
	a.captureHeuristic(failed, types.ExecutionOutput{Code: 1})
	notBuild := types.ToolCall{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"ls -la"}`}}
	a.captureHeuristic(notBuild, types.ExecutionOutput{Code: 0})
	entries, _ := store.Load()
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %+v", entries)
	}
}
