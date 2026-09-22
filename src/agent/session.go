package agent

import (
	"encoding/json"
	"fmt"
	"log"
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
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	Model     string    `json:"model"`
}

func randomUint() uint {
	return rand.Uint()
}

func newSession(model string, systemMsg string) *Session {
	session := &Session{
		Id: randomUint(),
		Messages: []types.Message{
			{Role: "system", Content: systemMsg},
		},
		CreatedAt: time.Now(),
		model:     model,
	}
	f, err := os.OpenFile(filepath.Join(config.SessionsDir, fmt.Sprintf("session_%d.jsonl", session.Id)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open session file: %v", err)
	}
	session.w = f
	return session
}

func (s *Session) Append(msg types.Message) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
	rec := sessionRecord{Timestamp: time.Now(), Type: msg.Role, Content: msg.Content, Model: s.model}
	line, err := json.Marshal(rec)
	if err != nil {
		log.Fatalf("Failed to marshal session record: %v", err)
	}
	_, err = s.w.Write(append(line, '\n'))
	if err != nil {
		log.Fatalf("Failed to write session record: %v", err)
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
