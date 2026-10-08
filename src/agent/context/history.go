package stubbs_context

import "stubbs/src/types"

type History struct {
	MaxMessages int
	Messages    []types.Message
}

func NewHistory(maxMessages int) *History {
	return &History{
		MaxMessages: maxMessages,
		Messages:    make([]types.Message, 0, maxMessages),
	}
}

func (h *History) Add(role, content string) {
	h.Messages = append(h.Messages, types.Message{
		Role:    role,
		Content: content,
	})

	h.trim()
}

func (h *History) AddMessage(msg types.Message) {
	h.Messages = append(h.Messages, msg)
	h.trim()
}

func (h *History) MessagesCopy() []types.Message {
	out := make([]types.Message, len(h.Messages))
	copy(out, h.Messages)
	return out
}

func (h *History) trim() {
	if h.MaxMessages <= 0 {
		return
	}

	if len(h.Messages) <= h.MaxMessages {
		return
	}

	start := len(h.Messages) - h.MaxMessages
	h.Messages = append([]types.Message(nil), h.Messages[start:]...)
}

func (h *History) Clear() {
	h.Messages = nil
}
