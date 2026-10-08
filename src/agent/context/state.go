package stubbs_context

import "strings"

type AgentState struct {
	Goal         string
	Plan         []string
	Completed    []string
	Current      string
	Blockers     []string
	FilesChanged []string
	// Add more fields as needed
}

func (s *AgentState) AddCompleted(item string) {
	item = strings.TrimSpace(item)
	if item == "" {
		return
	}

	for _, existing := range s.Completed {
		if existing == item {
			return
		}
	}

	s.Completed = append(s.Completed, item)
}

func (s *AgentState) AddFileChanged(file string) {
	file = strings.TrimSpace(file)
	if file == "" {
		return
	}

	for _, existing := range s.FilesChanged {
		if existing == file {
			return
		}
	}

	s.FilesChanged = append(s.FilesChanged, file)
}

func (s *AgentState) AddBlocker(blocker string) {
	blocker = strings.TrimSpace(blocker)
	if blocker == "" {
		return
	}

	for _, existing := range s.Blockers {
		if existing == blocker {
			return
		}
	}

	s.Blockers = append(s.Blockers, blocker)
}
