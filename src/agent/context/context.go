package stubbs_context

import (
	"fmt"
	"strings"
	"stubbs/src/prompts"
	"stubbs/src/types"
)

type Context struct {
	SystemPrompt string
	Task         string

	State           AgentState
	History         *History
	Summary         string
	ModelTokenLimit int
	CompactionRatio float64
}

func New(
	systemPrompt string,
	task string,
	modelTokenLimit int,
) *Context {
	return &Context{
		SystemPrompt:    systemPrompt,
		Task:            task,
		ModelTokenLimit: modelTokenLimit,
		CompactionRatio: 0.70,
		History:         NewHistory(12),
		State: AgentState{
			Goal: task,
		},
	}
}

func (c *Context) AddUser(content string) {
	c.History.Add("user", content)
}

func (c *Context) AddAssistant(content string) {
	c.History.Add("assistant", content)
}

func (c *Context) AddTool(content string) {
	// Tool output should ALWAYS be truncated before reaching history.
	content = truncateToolOutput(content, 8000)

	c.History.Add("tool", content)
}

// AddMessage appends a full message to the history, preserving tool call
// metadata (id/name/tool_calls) that the flat Add* helpers drop. Tool output is
// still truncated before it reaches history.
func (c *Context) AddMessage(msg types.Message) {
	if msg.Role == types.RoleTool {
		msg.Content = truncateToolOutput(msg.Content, 8000)
	}
	c.History.AddMessage(msg)
}

func (c *Context) Build() []types.Message {
	messages := make([]types.Message, 0, 4+len(c.History.Messages))
	if c.SystemPrompt != "" {
		messages = append(messages, types.Message{
			Role:    "system",
			Content: c.SystemPrompt,
		})
	}
	messages = append(messages, types.Message{
		Role:    "user",
		Content: "TASK:\n" + c.Task,
	})

	// 3. Current state
	messages = append(messages, types.Message{
		Role:    "system",
		Content: c.renderState(),
	})
	if c.Summary != "" {
		messages = append(messages, types.Message{
			Role:    "system",
			Content: "PREVIOUS WORK SUMMARY:\n" + c.Summary,
		})
	}

	// 5. Recent trajectory
	messages = append(messages, c.History.MessagesCopy()...)
	return messages
}

func (c *Context) renderState() string {
	var b strings.Builder

	b.WriteString("CURRENT AGENT STATE:\n")

	if c.State.Goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n", c.State.Goal)
	}

	if len(c.State.Plan) > 0 {
		b.WriteString("\nPlan:\n")
		for i, item := range c.State.Plan {
			fmt.Fprintf(&b, "%d. %s\n", i+1, item)
		}
	}

	if len(c.State.Completed) > 0 {
		b.WriteString("\nCompleted:\n")
		for _, item := range c.State.Completed {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}

	if c.State.Current != "" {
		fmt.Fprintf(&b, "\nCurrent: %s\n", c.State.Current)
	}

	if len(c.State.Blockers) > 0 {
		b.WriteString("\nBlockers:\n")
		for _, item := range c.State.Blockers {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}

	if len(c.State.FilesChanged) > 0 {
		b.WriteString("\nFiles changed:\n")
		for _, item := range c.State.FilesChanged {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}

	return b.String()
}

func (c *Context) PrepareForCompaction() []types.Message {
	old := c.History.MessagesCopy()
	messages := []types.Message{
		{
			Role:    "system",
			Content: prompts.CompactionPrompt,
		},
		{
			Role:    "user",
			Content: renderMessages(old),
		},
	}
	return messages
}

func renderMessages(messages []types.Message) string {
	var b strings.Builder

	for _, m := range messages {
		fmt.Fprintf(
			&b,
			"[%s]\n%s\n\n",
			m.Role,
			m.Content,
		)
	}

	return b.String()
}

func (c *Context) ApplySummary(summary string) {
	c.Summary = strings.TrimSpace(summary)

	// Throw away old trajectory.
	c.History.Clear()
}
