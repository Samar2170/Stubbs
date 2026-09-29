package tools

import (
	"context"
	"strings"
	"testing"

	"stubbs/src/memory"
)

func newMemoryTool(t *testing.T) (*MemoryTool, *memory.Store) {
	t.Helper()
	store, err := memory.NewStore(memory.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return NewMemoryTool(store), store
}

func TestMemoryToolAddSearchDelete(t *testing.T) {
	tool, store := newMemoryTool(t)

	out := tool.Execute(context.Background(), `{"action":"add","kind":"project-fact","title":"Build","body":"go build ./...","tags":["build"]}`)
	if out.Code != 0 || out.Error != "" {
		t.Fatalf("add: %+v", out)
	}

	out = tool.Execute(context.Background(), `{"action":"search","query":"build"}`)
	if out.Code != 0 {
		t.Fatalf("search: %+v", out)
	}
	if !strings.Contains(out.Output, "go build") {
		t.Fatalf("search output = %q", out.Output)
	}

	entries, err := store.Load()
	if err != nil || len(entries) != 1 {
		t.Fatalf("load = %+v, %v", entries, err)
	}

	out = tool.Execute(context.Background(), `{"action":"delete","id":"`+entries[0].ID+`"}`)
	if out.Code != 0 {
		t.Fatalf("delete: %+v", out)
	}
	after, _ := store.Load()
	if len(after) != 0 {
		t.Fatalf("expected empty store, got %d entries", len(after))
	}
}

func TestMemoryToolErrors(t *testing.T) {
	tool, _ := newMemoryTool(t)

	if out := tool.Execute(context.Background(), `{"action":"bogus"}`); out.Code == 0 {
		t.Error("unknown action should fail")
	}
	if out := tool.Execute(context.Background(), `{"action":"add","title":"x"}`); out.Code == 0 {
		t.Error("add without body should fail")
	}
	if out := tool.Execute(context.Background(), `{"action":"delete"}`); out.Code == 0 {
		t.Error("delete without id should fail")
	}
	if out := tool.Execute(context.Background(), `not json`); out.Code == 0 {
		t.Error("malformed args should fail")
	}
}

func TestMemoryToolSearchEmpty(t *testing.T) {
	tool, _ := newMemoryTool(t)
	out := tool.Execute(context.Background(), `{"action":"search","query":"nothing"}`)
	if out.Code != 0 {
		t.Fatalf("search: %+v", out)
	}
	if !strings.Contains(out.Output, "No matching") {
		t.Fatalf("output = %q", out.Output)
	}
}
