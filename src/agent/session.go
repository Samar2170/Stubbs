package agent

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"stubbs/src/config"
	"stubbs/src/types"
	"sync"
	"time"
)

type Session struct {
	Id        uint
	Messages  []types.Message
	context   Context
	CreatedAt time.Time
	mu        sync.Mutex
	model     string
	w         *os.File
}

type sessionRecord struct {
	Timestamp  time.Time        `json:"timestamp"`
	Type       string           `json:"type"`
	Content    string           `json:"content"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCalls  []types.ToolCall `json:"tool_calls,omitempty"`
	Error      string           `json:"error,omitempty"`
	Code       int              `json:"code,omitempty"`
	DurationMS int64            `json:"duration_ms,omitempty"`
	Model      string           `json:"model"`
}

func randomUint() uint {
	return rand.Uint()
}

func newSession(model string, systemMsg string) (*Session, error) {
	session := &Session{
		Id: randomUint(),
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: systemMsg},
		},
		CreatedAt: time.Now(),
		model:     model,
		context:   NewContext(systemMsg),
	}
	f, err := os.OpenFile(filepath.Join(config.SessionsDir, fmt.Sprintf("session_%d.jsonl", session.Id)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	session.w = f
	return session, nil
}

func (s *Session) Append(msg types.Message) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
	s.context.AddMessage(msg)
	rec := sessionRecord{
		Timestamp:  time.Now(),
		Type:       msg.Role,
		Content:    msg.Content,
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
		ToolCalls:  msg.ToolCalls,
		Model:      s.model,
	}
	return s.write(rec)
}

// AppendToolResult records a tool execution with its structured outcome
// (name, error, exit code, duration) and appends the matching tool message to
// the API history. The rendered text stays in Content so the session log is
// still readable; the extra fields make failures diagnosable.
func (s *Session) AppendToolResult(call types.ToolCall, out types.ExecutionOutput) error {
	if s == nil {
		return nil
	}
	msg := types.Message{
		Role:       types.RoleTool,
		Content:    renderExecution(out),
		ToolCallID: call.ID,
		Name:       call.Function.Name,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
	s.context.AddMessage(msg)
	rec := sessionRecord{
		Timestamp:  time.Now(),
		Type:       msg.Role,
		Content:    msg.Content,
		ToolCallID: call.ID,
		Name:       call.Function.Name,
		Error:      out.Error,
		Code:       out.Code,
		DurationMS: out.Duration.Milliseconds(),
		Model:      s.model,
	}
	return s.write(rec)
}

// AppendError records a non-fatal error (model call failure, retry, malformed
// response). It never touches the API history so corrective events cannot
// pollute the conversation.
func (s *Session) AppendError(scope string, err error) error {
	if s == nil || err == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content := err.Error()
	if scope != "" {
		content = scope + ": " + content
	}
	return s.write(sessionRecord{Timestamp: time.Now(), Type: "error", Content: content, Model: s.model})
}

// write marshals and appends a record. The caller must hold s.mu.
func (s *Session) write(rec sessionRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal session record: %w", err)
	}
	if _, err := s.w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write session record: %w", err)
	}
	return s.w.Sync()
}

func (s *Session) ContextMessages(budget int) []types.Message {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.context.Build(budget)
}

func (s *Session) InjectMemory(content string, importance float32) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.context.AddMemory(content, importance)
}

// SetModel updates the model recorded on subsequent session entries.
func (s *Session) SetModel(model string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = model
}

func (s *Session) History() []types.Message {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]types.Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

func (s *Session) Close() error {
	if s == nil || s.w == nil {
		return nil
	}
	err := s.w.Close()
	s.w = nil
	return err
}

// func (s *Session) SetSystemMessage(msg string) {
// 	s.mu.Lock()
// 	defer s.mu.Unlock()
// 	s.Messages = append([]types.Message{{Role: "system", Content: msg}}, s.Messages...)
// }
