package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"stubbs/src/types"
)

// reduce must drop the fewest turns that make the context fit, not every
// evictable turn (the previous running-total bug discarded all of them).
func TestContextReduceDropsOnlyWhatIsNeeded(t *testing.T) {
	turn := strings.Repeat("word ", 1000)
	perTurn := estimateTokens(turn)

	c := NewContext("sys")
	c.AddMessage(types.Message{Role: types.RoleUser, Content: "task"})
	for i := 0; i < 10; i++ {
		c.AddMessage(types.Message{Role: types.RoleAssistant, Content: turn})
	}
	total := c.totalTokens()

	kept, dropped := c.BuildWithDropped(total - perTurn + 1)
	if len(dropped) != 1 {
		t.Fatalf("dropped %d items, want exactly 1", len(dropped))
	}
	if len(kept) != 11 { // system + task + 9 assistants
		t.Fatalf("kept %d messages, want 11", len(kept))
	}
	if kept[0].Role != types.RoleSystem {
		t.Fatalf("system prompt should survive, got %q", kept[0].Role)
	}
}

func TestContextReduceKeepsPinnedTask(t *testing.T) {
	turn := strings.Repeat("word ", 1000)
	c := NewContext("sys")
	c.AddMessage(types.Message{Role: types.RoleUser, Content: "the pinned task"})
	for i := 0; i < 10; i++ {
		c.AddMessage(types.Message{Role: types.RoleAssistant, Content: turn})
	}
	kept, _ := c.BuildWithDropped(100)
	var sawTask bool
	for _, m := range kept {
		if m.Content == "the pinned task" {
			sawTask = true
		}
	}
	if !sawTask {
		t.Fatal("pinned task must never be dropped")
	}
}

func TestCompactDroppedSummarizesAndInjects(t *testing.T) {
	fc := &fakeCompleter{content: "- found the bug in foo.go"}
	s := &Session{}
	s.context = NewContext("sys")
	s.context.AddMessage(types.Message{Role: types.RoleUser, Content: "task"})
	for i := 0; i < 12; i++ {
		s.context.AddMessage(types.Message{Role: types.RoleAssistant, Content: strings.Repeat("word ", 1000)})
	}
	a := &Agent{
		ModelClient: fc,
		config:      &AgentConfig{ContextBudget: 2000},
		Session:     s,
	}

	msgs := a.getMessages(context.Background())
	if !fc.called {
		t.Fatal("compaction should call the model once enough is dropped")
	}
	if len(msgs) == 0 {
		t.Fatal("expected a non-empty context after compaction")
	}
	var sawSummary bool
	for _, it := range s.context.Items {
		if it.Type == Plan && strings.Contains(it.Message.Content, "found the bug in foo.go") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Fatal("compaction digest should be injected as a pinned Plan item")
	}
}

func TestCompactDroppedSkipsTinyDrops(t *testing.T) {
	fc := &fakeCompleter{content: "should not be called"}
	a := &Agent{ModelClient: fc}
	dropped := []types.Message{{Role: types.RoleAssistant, Content: "small"}}
	if got := a.compactDropped(context.Background(), dropped); got != "" {
		t.Fatalf("tiny drop should not summarize, got %q", got)
	}
	if fc.called {
		t.Fatal("model should not be called for a tiny drop")
	}
}

func TestRepeatedCallGuard(t *testing.T) {
	ia := &InteractiveAgent{}
	call := []types.ToolCall{{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}}}
	for i := 0; i < maxRepeatedCalls; i++ {
		if ia.repeatedCall(call) {
			t.Fatalf("flagged too early at repeat %d", i+1)
		}
	}
	if !ia.repeatedCall(call) {
		t.Fatal("identical consecutive calls should be flagged after the threshold")
	}
	other := []types.ToolCall{{Function: types.FunctionCall{Name: "read", Arguments: `{"file_path":"x"}`}}}
	if ia.repeatedCall(other) {
		t.Fatal("a different call should reset the repeat counter")
	}
}

func TestCheckBudgetHonorsMaxModelCalls(t *testing.T) {
	ia := &InteractiveAgent{
		Agent: &Agent{ModelCalls: 3},
		cfg:   InteractiveConfig{AgentConfig: AgentConfig{MaxModelCalls: 3}},
	}
	if err := ia.checkBudget(); !errors.Is(err, ErrLimitsExceeded) {
		t.Fatalf("checkBudget = %v, want ErrLimitsExceeded", err)
	}
	ia.ModelCalls = 2
	if err := ia.checkBudget(); err != nil {
		t.Fatalf("below the cap checkBudget = %v, want nil", err)
	}
}
