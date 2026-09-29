package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"stubbs/src/memory"
	"stubbs/src/types"
)

type MemoryTool struct {
	Store *memory.Store
}

func NewMemoryTool(store *memory.Store) *MemoryTool {
	return &MemoryTool{Store: store}
}

func (t *MemoryTool) Name() string { return "memory" }

func (t *MemoryTool) Description() string {
	return "Read and write the agent's persistent project memory, which is " +
		"remembered across sessions. Use \"search\" to recall project facts, " +
		"conventions, build/test commands, user preferences or past work; use " +
		"\"add\" to store a durable fact worth keeping; use \"delete\" to remove " +
		"an outdated entry by id."
}

func (t *MemoryTool) Parameters() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"search", "add", "delete"},
				"description": "The operation to perform.",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Search terms (search).",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Entry id to remove (delete).",
			},
			"kind": map[string]any{
				"type":        "string",
				"enum":        []string{"project-fact", "preference", "procedure", "episode"},
				"description": "Entry category (add).",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Short label for the entry (add).",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "The durable fact, standalone and concise (add).",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional lowercase keywords (add).",
			},
		},
		"required": []string{"action"},
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return []byte(`{"type":"object"}`)
	}
	return encoded
}

type memoryArgs struct {
	Action string   `json:"action"`
	Query  string   `json:"query"`
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Tags   []string `json:"tags"`
}

func (t *MemoryTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	if t == nil || t.Store == nil {
		return types.ExecutionOutput{Error: "memory: store is not configured", Code: -1}
	}
	var in memoryArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("memory: bad arguments: %v", err), Code: -1}
	}
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "search":
		return t.search(in.Query)
	case "add":
		return t.add(in)
	case "delete":
		return t.del(in.ID)
	default:
		return types.ExecutionOutput{Error: fmt.Sprintf("memory: unknown action %q", in.Action), Code: -1}
	}
}

func (t *MemoryTool) search(query string) types.ExecutionOutput {
	entries, err := t.Store.Search(query, 10)
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("memory: %v", err), Code: -1}
	}
	if len(entries) == 0 {
		return types.ExecutionOutput{Output: "No matching memory entries."}
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "[%s] (%s) %s\n", e.ID, e.Kind, e.Title)
		if strings.TrimSpace(e.Body) != "" {
			fmt.Fprintf(&b, "  %s\n", e.Body)
		}
	}
	return types.ExecutionOutput{Output: strings.TrimRight(b.String(), "\n")}
}

func (t *MemoryTool) add(in memoryArgs) types.ExecutionOutput {
	if strings.TrimSpace(in.Body) == "" {
		return types.ExecutionOutput{Error: "memory: 'body' is required for add", Code: -1}
	}
	kind := memory.Kind(strings.TrimSpace(in.Kind))
	switch kind {
	case memory.KindProjectFact, memory.KindPreference, memory.KindProcedure, memory.KindEpisode:
	default:
		kind = memory.KindProjectFact
	}
	e := memory.Entry{
		Kind:       kind,
		Title:      strings.TrimSpace(in.Title),
		Body:       strings.TrimSpace(in.Body),
		Tags:       in.Tags,
		Importance: 0.7,
	}
	if err := t.Store.Add(e); err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("memory: %v", err), Code: -1}
	}
	return types.ExecutionOutput{Output: fmt.Sprintf("Stored memory entry (%s).", e.Title)}
}

func (t *MemoryTool) del(id string) types.ExecutionOutput {
	if strings.TrimSpace(id) == "" {
		return types.ExecutionOutput{Error: "memory: 'id' is required for delete", Code: -1}
	}
	if err := t.Store.Delete(strings.TrimSpace(id)); err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("memory: %v", err), Code: -1}
	}
	return types.ExecutionOutput{Output: fmt.Sprintf("Deleted memory entry %s.", id)}
}
