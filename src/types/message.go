package types

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
