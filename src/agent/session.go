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
	CreatedAt time.Time
	mu        sync.Mutex
	model     string
	w         *os.File
}

type sessionRecord struct {
	Timestamp  time.Time `json:"timestamp"`
	Type       string    `json:"type"`
	Content    string    `json:"content"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Model      string    `json:"model"`
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
	rec := sessionRecord{Timestamp: time.Now(), Type: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID, Model: s.model}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal session record: %w", err)
	}
	_, err = s.w.Write(append(line, '\n'))
	if err != nil {
		return fmt.Errorf("write session record: %w", err)
	}
	return s.w.Sync()

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
