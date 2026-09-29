package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stubbs/src/memory"
	"stubbs/src/types"
)

func TestAddMemoryInsertsAfterSystem(t *testing.T) {
	c := NewContext("sys")
	c.AddMemory("remember this", 0.9)
	if len(c.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(c.Items))
	}
	if c.Items[0].Type != System {
		t.Fatalf("first item type = %v, want System", c.Items[0].Type)
	}
	m := c.Items[1]
	if m.Type != Memory {
		t.Errorf("type = %v, want Memory", m.Type)
	}
	if m.Message.Content != "remember this" {
		t.Errorf("content = %q", m.Message.Content)
	}
	if m.Pinned {
		t.Error("memory must not be pinned")
	}
	if m.Importance != 0.9 {
		t.Errorf("importance = %v, want 0.9", m.Importance)
	}
}

func TestAddMemoryIgnoresEmpty(t *testing.T) {
	c := NewContext("sys")
	c.AddMemory("   ", 1)
	if len(c.Items) != 1 {
		t.Fatalf("empty memory should be ignored, got %d items", len(c.Items))
	}
}

func TestMemoryIsEvictableUnderBudgetPressure(t *testing.T) {
	c := NewContext("sys")
	c.AddMessage(types.Message{Role: types.RoleUser, Content: "task"})
	c.AddMemory(strings.Repeat("recalled ", 4000), 1)
	for i := 0; i < 12; i++ {
		c.AddMessage(types.Message{Role: types.RoleAssistant, Content: fmt.Sprintf("turn %d", i)})
	}
	out := c.Build(50)
	for _, m := range out {
		if strings.Contains(m.Content, "recalled") {
			t.Fatal("memory should be evictable when the budget is exceeded")
		}
	}
}

func TestMemoryPreambleIncludesCoreAndRelevant(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewStore(memory.Options{Dir: dir, TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core", "preferences.md"), []byte("Never add comments."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(memory.Entry{Title: "Build", Body: "run go build ./...", Tags: []string{"build"}}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{Memory: store}
	got := a.memoryPreamble("how do I build?")
	if !strings.Contains(got, "Never add comments.") {
		t.Fatalf("core memory missing: %q", got)
	}
	if !strings.Contains(got, "run go build") {
		t.Fatalf("relevant memory missing: %q", got)
	}
}

func TestMemoryPreambleRespectsBudget(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewStore(memory.Options{Dir: dir, BudgetTokens: 1, TopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(memory.Entry{Title: "Build", Body: strings.Repeat("word ", 100)}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{Memory: store}
	if got := a.memoryPreamble("build"); got != "" {
		t.Fatalf("expected over-budget memory to be dropped, got %q", got)
	}
}

func TestMemoryPreambleNilStore(t *testing.T) {
	a := &Agent{}
	if got := a.memoryPreamble("anything"); got != "" {
		t.Fatalf("nil store should yield empty preamble, got %q", got)
	}
}
