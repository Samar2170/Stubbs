package types

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}
