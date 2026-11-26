package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func mentionModel(t *testing.T) *model {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"main.go", "README.md", "src/tui.go"} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := testModel()
	m.opts.Workdir = root
	m.pending = &pendingInput{kind: inComment, reply: make(chan inputResult, 1)}
	return m
}

func TestMentionQuery(t *testing.T) {
	cases := []struct {
		text  string
		query string
		start int
		ok    bool
	}{
		{"@", "", 0, true},
		{"@mai", "mai", 0, true},
		{"explain @main", "main", 8, true},
		{"foo@bar", "", 0, false},
		{"@main.go ", "", 0, false},
		{"plain", "", 0, false},
	}
	for _, tc := range cases {
		query, start, ok := mentionQuery(tc.text)
		if ok != tc.ok || query != tc.query || (ok && start != tc.start) {
			t.Errorf("mentionQuery(%q) = %q, %d, %v; want %q, %d, %v",
				tc.text, query, start, ok, tc.query, tc.start, tc.ok)
		}
	}
}

func TestMentionMenuOpensAndFilters(t *testing.T) {
	m := mentionModel(t)
	typeKeys(m, "@mai")
	if !m.menu.open {
		t.Fatal("typing '@mai' should open the file menu")
	}
	if m.menu.mode != menuFiles {
		t.Fatalf("menu mode = %v, want menuFiles", m.menu.mode)
	}
	if len(m.menu.options) != 1 || m.menu.options[0].name != "@main.go" {
		t.Fatalf("filter '@mai' = %+v, want @main.go", m.menu.options)
	}
}

func TestMentionMenuMatchesBaseName(t *testing.T) {
	m := mentionModel(t)
	typeKeys(m, "@tui")
	if len(m.menu.options) != 1 || m.menu.options[0].name != "@src/tui.go" {
		t.Fatalf("filter '@tui' = %+v, want @src/tui.go", m.menu.options)
	}
}

func TestMentionTabCompletesWithoutSubmitting(t *testing.T) {
	m := mentionModel(t)
	reply := make(chan inputResult, 1)
	m.pending.reply = reply
	typeKeys(m, "look at @mai")
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.ta.Value(); got != "look at @main.go " {
		t.Fatalf("tab completed %q, want %q", got, "look at @main.go ")
	}
	if len(reply) != 0 {
		t.Error("file completion must not submit the prompt")
	}
	if m.menu.open {
		t.Error("menu should close once the path is completed")
	}
}

func TestMentionMenuHiddenForShellCommands(t *testing.T) {
	m := mentionModel(t)
	m.pending.kind = inCommand
	typeKeys(m, "@")
	if m.menu.open {
		t.Error("file mentions should not open while entering a shell command")
	}
}

func TestMentionMenuShowsInView(t *testing.T) {
	m := mentionModel(t)
	m.w, m.h = 80, 30
	m.layout()
	typeKeys(m, "@")
	if !strings.Contains(m.View(), "@main.go") {
		t.Error("the view should include the file menu")
	}
	if m.menuHeight() == 0 {
		t.Error("file menu should reserve layout rows")
	}
}
