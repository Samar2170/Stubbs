package agent

import (
	"strings"
	"stubbs/src/types"
	"time"
	"unicode/utf8"
)

const MODEL_LIMIT = 1000000

var contextBudget = MODEL_LIMIT * 0.5

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
	ID          string
	Role        string
	Content     string
	Tokens      int
	Importance  float32
	Timestamp   time.Time
	Refetchable bool
	Type        ItemType
}

func NewContext(sysTemplate string) Context {
	ci := ContextItem{
		ID:          "system-prompt",
		Role:        types.RoleSystem,
		Content:     sysTemplate,
		Tokens:      estimateTokens(sysTemplate),
		Importance:  0,
		Timestamp:   time.Now(),
		Refetchable: false,
		Type:        System,
	}
	return Context{Items: []ContextItem{ci}}
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	words := strings.Fields(s)
	chars := utf8.RuneCountInString(s)
	return max(len(words), (chars+3)/4)
}
