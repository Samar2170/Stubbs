package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"stubbs/src/llm"
	"stubbs/src/memory"
	"stubbs/src/prompts"
	"stubbs/src/types"
)

const (
	memoryMinimumTokens  = 200
	memorySummaryTimeout = 90 * time.Second
	memoryExistingLimit  = 4000
)

var (
	secretPattern = regexp.MustCompile(`(?im)^([A-Za-z0-9_ .-]*(?:KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL)[A-Za-z0-9_ .-]*\s*[:=]\s*)\S.*$`)

	buildTestPattern = regexp.MustCompile(`\b(go build|go test|go vet|npm (test|run)|yarn (test|build)|pnpm (test|build)|pytest|cargo (test|build)|make|mvn (test|package)|gradle (test|build|assemble))\b`)
)

type textCompleter interface {
	Complete(context.Context, []types.Message) (llm.ORChatResponse, error)
}

// SummarizeMemory distils the active session into persistent memory entries.
// It is not run automatically: callers invoke it only for runs that finished
// cleanly, and off the critical path, so completion is never delayed. The
// passed context bounds the (otherwise fixed timeout) summary call and should
// stay alive until the summary has been written.
func (a *Agent) SummarizeMemory(ctx context.Context) {
	if a == nil || a.Memory == nil || a.Session == nil || a.ModelClient == nil {
		return
	}
	if !a.Memory.Options().AutoSummarize {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	transcript := redactSecrets(renderTranscript(a.Session.History()))
	if estimateTokens(transcript) < memoryMinimumTokens {
		return
	}

	req := []types.Message{
		{Role: types.RoleSystem, Content: prompts.Memory},
		{Role: types.RoleUser, Content: fmt.Sprintf(
			"Existing memory entries (do not duplicate):\n%s\n\nSession transcript:\n%s",
			a.existingMemory(), transcript)},
	}

	cctx, cancel := context.WithTimeout(ctx, memorySummaryTimeout)
	defer cancel()

	content, err := a.completeText(cctx, req)
	if err != nil {
		return
	}
	for _, e := range parseMemoryEntries(content) {
		_ = a.Memory.Add(e)
	}
}

func (a *Agent) completeText(ctx context.Context, messages []types.Message) (string, error) {
	if c, ok := a.ModelClient.(textCompleter); ok {
		resp, err := c.Complete(ctx, messages)
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("agent: memory summary response has no choices")
		}
		return resp.Choices[0].Message.Content, nil
	}
	resp, err := a.ModelClient.CompleteText(ctx, messages)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("agent: memory summary response has no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

func (a *Agent) existingMemory() string {
	entries, err := a.Memory.Load()
	if err != nil || len(entries) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", e.Kind, e.Title, oneLine(e.Body))
	}
	out := b.String()
	if len(out) > memoryExistingLimit {
		out = out[:memoryExistingLimit]
	}
	return out
}

func (a *Agent) captureHeuristic(call types.ToolCall, out types.ExecutionOutput) {
	if a == nil || a.Memory == nil || !a.Memory.Options().CaptureHeuristics {
		return
	}
	if out.Code != 0 || out.Error != "" {
		return
	}
	cmd := strings.TrimSpace(CommandOf(call))
	if cmd == "" || !buildTestPattern.MatchString(cmd) {
		return
	}
	if existing, err := a.Memory.Search(cmd, 5); err == nil {
		for _, e := range existing {
			if strings.Contains(e.Body, cmd) {
				return
			}
		}
	}
	_ = a.Memory.Add(memory.Entry{
		Kind:       memory.KindProcedure,
		Title:      "Build/test command",
		Body:       cmd,
		Tags:       []string{"build", "command"},
		Importance: 0.6,
		Source:     "heuristic",
	})
}

type rawMemoryEntry struct {
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Tags       []string `json:"tags"`
	Importance float32  `json:"importance"`
}

func parseMemoryEntries(s string) []memory.Entry {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			s = s[i : j+1]
		}
	}
	var raws []rawMemoryEntry
	if err := json.Unmarshal([]byte(s), &raws); err != nil {
		return nil
	}
	out := make([]memory.Entry, 0, len(raws))
	for _, r := range raws {
		body := strings.TrimSpace(r.Body)
		title := strings.TrimSpace(r.Title)
		if body == "" && title == "" {
			continue
		}
		kind := memory.Kind(r.Kind)
		switch kind {
		case memory.KindProjectFact, memory.KindPreference, memory.KindProcedure, memory.KindEpisode:
		default:
			kind = memory.KindProjectFact
		}
		out = append(out, memory.Entry{
			Kind:       kind,
			Title:      title,
			Body:       body,
			Tags:       r.Tags,
			Importance: r.Importance,
		})
	}
	return out
}

func renderTranscript(msgs []types.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == types.RoleSystem {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "  tool_call %s(%s)\n", tc.Function.Name, tc.Function.Arguments)
		}
	}
	return b.String()
}

func redactSecrets(s string) string {
	return secretPattern.ReplaceAllString(s, "${1}[REDACTED]")
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
