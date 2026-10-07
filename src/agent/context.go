package agent

import (
	"fmt"
	"sort"
	"strings"
	"stubbs/src/types"
	"time"
	"unicode/utf8"
)

// DefaultContextBudget caps how many tokens of history are sent to the model
// per request. Keeping it small is what keeps per-turn latency flat: the whole
// transcript is resent every turn, so an unbounded budget makes each turn
// slower than the last.
const DefaultContextBudget = 32000

const minRecentTurns = 4

type ItemType int

const (
	System ItemType = iota
	Task
	Memory
	Plan
	RecentHistory
	ToolResults
	Buffer
)

type Context struct {
	Items []ContextItem
}

type ContextItem struct {
	Message     types.Message
	ID          string
	Tokens      int
	Importance  float32
	Timestamp   time.Time
	Refetchable bool
	Pinned      bool
	Type        ItemType
}

func NewContext(sysTemplate string) Context {
	c := Context{}
	c.Items = append(c.Items, ContextItem{
		Message:   types.Message{Role: types.RoleSystem, Content: sysTemplate},
		ID:        "system-prompt",
		Tokens:    estimateTokens(sysTemplate),
		Timestamp: time.Now(),
		Pinned:    true,
		Type:      System,
	})
	return c
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	words := strings.Fields(s)
	chars := utf8.RuneCountInString(s)
	return max(len(words), (chars+3)/4)
}

func (c *Context) AddMessage(msg types.Message) {
	if c == nil {
		return
	}
	typ := RecentHistory
	pinned := false
	switch msg.Role {
	case types.RoleSystem:
		typ, pinned = System, true
	case types.RoleTool:
		typ = ToolResults
	case types.RoleUser:
		if !c.hasType(Task) {
			typ, pinned = Task, true
		}
	}
	c.Items = append(c.Items, ContextItem{
		Message:   msg,
		ID:        fmt.Sprintf("item-%d", len(c.Items)),
		Tokens:    estimateTokens(msg.Content),
		Timestamp: time.Now(),
		Pinned:    pinned,
		Type:      typ,
	})

}

func (c *Context) AddMemory(content string, importance float32) {
	if c == nil || strings.TrimSpace(content) == "" {
		return
	}
	item := ContextItem{
		Message:    types.Message{Role: types.RoleSystem, Content: content},
		ID:         fmt.Sprintf("memory-%d", len(c.Items)),
		Tokens:     estimateTokens(content),
		Importance: importance,
		Timestamp:  time.Now(),
		Type:       Memory,
	}
	pos := 1
	if pos > len(c.Items) {
		pos = len(c.Items)
	}
	c.Items = append(c.Items, ContextItem{})
	copy(c.Items[pos+1:], c.Items[pos:])
	c.Items[pos] = item
}

// AddSummary records a digest of turns that were compacted out of the context.
// It is pinned (Type Plan) so it survives later reductions and the model keeps
// the gist of work that is no longer verbatim in the transcript.
func (c *Context) AddSummary(content string) {
	if c == nil || strings.TrimSpace(content) == "" {
		return
	}
	item := ContextItem{
		Message:   types.Message{Role: types.RoleSystem, Content: content},
		ID:        fmt.Sprintf("summary-%d", len(c.Items)),
		Tokens:    estimateTokens(content),
		Timestamp: time.Now(),
		Pinned:    true,
		Type:      Plan,
	}
	pos := 1
	if pos > len(c.Items) {
		pos = len(c.Items)
	}
	c.Items = append(c.Items, ContextItem{})
	copy(c.Items[pos+1:], c.Items[pos:])
	c.Items[pos] = item
}

func (c *Context) hasType(t ItemType) bool {
	for i := range c.Items {
		if c.Items[i].Type == t {
			return true
		}
	}
	return false
}

func (c *Context) totalTokens() int {
	total := 0
	for i := range c.Items {
		total += c.Items[i].Tokens
	}
	return total
}

func (c *Context) Build(budget int) []types.Message {
	kept, _ := c.BuildWithDropped(budget)
	return kept
}

// BuildWithDropped reduces the context to fit budget and returns both the
// surviving messages and the messages that were dropped, so the caller can
// summarize them into a compact digest instead of losing the information.
func (c *Context) BuildWithDropped(budget int) (kept []types.Message, dropped []types.Message) {
	if c == nil {
		return nil, nil
	}
	dropped = c.reduce(budget)
	out := make([]types.Message, 0, len(c.Items))
	for i := range c.Items {
		out = append(out, c.Items[i].Message)
	}
	return out, dropped
}

type turn struct {
	start int
	end   int
}

func groupTurns(items []ContextItem) []turn {
	turns := make([]turn, 0, len(items))
	for i := 0; i < len(items); {
		start := i
		m := items[i].Message
		i++
		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			for i < len(items) && items[i].Message.Role == types.RoleTool {
				i++
			}
		}
		turns = append(turns, turn{start: start, end: i})
	}
	return turns
}

func turnPinned(items []ContextItem, t turn) bool {
	for i := t.start; i < t.end; i++ {
		if items[i].Pinned {
			return true
		}
		switch items[i].Type {
		case System, Task, Plan:
			return true
		}
	}
	return false
}

func typeWeight(t ItemType) float32 {
	switch t {
	case Memory, Plan:
		return 2
	case RecentHistory:
		return 1
	case ToolResults:
		return 0.5
	default:
		return 0
	}
}

func turnScore(items []ContextItem, t turn, rank, total int) float32 {
	score := float32(rank+1) / float32(total)
	for i := t.start; i < t.end; i++ {
		s := typeWeight(items[i].Type) + items[i].Importance
		if items[i].Refetchable {
			s -= 0.25
		}
		if s > score {
			score = s
		}
	}
	return score
}

// reduce drops the lowest-scoring unpinned turns until the context fits the
// budget, and returns the messages it dropped. It drops the fewest turns that
// make the context fit: a running total is decremented as each turn is marked,
// so it never discards more history than the budget requires.
func (c *Context) reduce(budget int) []types.Message {
	if budget <= 0 || c.totalTokens() <= budget {
		return nil
	}
	turns := groupTurns(c.Items)
	if len(turns) == 0 {
		return nil
	}
	protected := len(turns) - minRecentTurns
	if protected < 0 {
		protected = 0
	}
	candidates := make([]int, 0, len(turns))
	for i, t := range turns {
		if i >= protected || turnPinned(c.Items, t) {
			continue
		}
		candidates = append(candidates, i)
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		ia, ib := candidates[a], candidates[b]
		return turnScore(c.Items, turns[ia], ia, len(turns)) <
			turnScore(c.Items, turns[ib], ib, len(turns))
	})
	drop := make([]bool, len(c.Items))
	remaining := c.totalTokens()
	for _, idx := range candidates {
		if remaining <= budget {
			break
		}
		t := turns[idx]
		for i := t.start; i < t.end; i++ {
			drop[i] = true
			remaining -= c.Items[i].Tokens
		}
	}
	kept := make([]ContextItem, 0, len(c.Items))
	var dropped []types.Message
	for i := range c.Items {
		if drop[i] {
			dropped = append(dropped, c.Items[i].Message)
			continue
		}
		kept = append(kept, c.Items[i])
	}
	c.Items = kept
	return dropped
}
