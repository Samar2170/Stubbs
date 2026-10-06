package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"stubbs/src/config"
	"stubbs/src/types"
)

// SessionInfo is a lightweight summary of one persisted session log. It backs
// the /sessions picker: the first message the user sent, when the session was
// last active, and how many records it holds.
type SessionInfo struct {
	ID           string    `json:"id"`
	FirstMessage string    `json:"first_message"`
	Updated      time.Time `json:"updated"`
	Messages     int       `json:"messages"`
}

// sessionPath returns the log path for a session id.
func sessionPath(id string) string {
	return filepath.Join(config.SessionsDir, fmt.Sprintf("session_%s.jsonl", id))
}

// ListSessions summarizes every session log under the sessions directory,
// ordered by most recent activity first (newest date at the top).
func ListSessions() ([]SessionInfo, error) {
	return listSessions(config.SessionsDir)
}

func listSessions(dir string) ([]SessionInfo, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "session_*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}
	out := make([]SessionInfo, 0, len(paths))
	for _, p := range paths {
		info, err := readSessionInfo(p)
		if err != nil {
			// A single unreadable/corrupt log should not hide the rest.
			continue
		}
		if info.Messages == 0 {
			// Empty logs (including the freshly-created current session) are
			// noise in a "previous sessions" list.
			continue
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// readSessionInfo scans a single log file without loading every message into
// memory. Records that fail to unmarshal are skipped so a partial line at the
// tail of a live log cannot poison the listing.
func readSessionInfo(path string) (SessionInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionInfo{}, err
	}
	defer f.Close()

	info := SessionInfo{ID: sessionIDFromPath(path)}
	var last time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec sessionRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		info.Messages++
		if !rec.Timestamp.IsZero() {
			last = rec.Timestamp
		}
		if info.FirstMessage == "" {
			content := strings.TrimSpace(rec.Content)
			if rec.Type == types.RoleUser && content != "" {
				info.FirstMessage = content
			}
		}
	}
	if err := sc.Err(); err != nil {
		return SessionInfo{}, fmt.Errorf("session: read %s: %w", path, err)
	}
	if !last.IsZero() {
		info.Updated = last
	} else if fi, err := os.Stat(path); err == nil {
		info.Updated = fi.ModTime()
	}
	return info, nil
}

func sessionIDFromPath(path string) string {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, ".jsonl")
	name = strings.TrimPrefix(name, "session_")
	return name
}

// LoadSession reopens a persisted session log for appending and reconstructs
// its messages. systemPrompt is the system prompt to resume with (typically
// SystemPromptFor of the current tools); the log does not persist it. The
// returned slice includes the system prompt so callers can rebuild the
// transcript.
//
// The JSONL log does not persist tool-call arguments, so a tool record cannot
// be paired with the assistant call that produced it. To keep the resumed
// conversation valid for the model, only plain user and non-empty assistant
// text is replayed into the context; tool records remain in Messages for
// display.
func LoadSession(model, id, systemPrompt string) (*Session, []types.Message, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil, fmt.Errorf("session: empty id")
	}
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = SYSTEM_TEMPLATE
	}
	path := sessionPath(id)
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("session: open %s: %w", id, err)
	}
	records, err := decodeSession(f)
	f.Close()
	if err != nil {
		return nil, nil, err
	}
	uid, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("session: bad id %q: %w", id, err)
	}

	s := &Session{
		Id:       uint(uid),
		Messages: []types.Message{{Role: types.RoleSystem, Content: systemPrompt}},
		model:    model,
		context:  NewContext(systemPrompt),
	}
	display := make([]types.Message, 0, len(records)+1)
	display = append(display, s.Messages[0])
	for _, rec := range records {
		msg := types.Message{Role: rec.Type, Content: rec.Content, ToolCallID: rec.ToolCallID, Name: rec.Name}
		s.Messages = append(s.Messages, msg)
		display = append(display, msg)
		if s.CreatedAt.IsZero() && !rec.Timestamp.IsZero() {
			s.CreatedAt = rec.Timestamp
		}
		switch rec.Type {
		case types.RoleUser:
			s.context.AddMessage(msg)
		case types.RoleAssistant:
			if strings.TrimSpace(rec.Content) != "" {
				s.context.AddMessage(msg)
			}
		}
	}
	w, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("session: reopen %s: %w", id, err)
	}
	s.w = w
	return s, display, nil
}

func decodeSession(r io.Reader) ([]sessionRecord, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out []sessionRecord
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec sessionRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("session: read log: %w", err)
	}
	return out, nil
}
