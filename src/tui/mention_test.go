package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestScanMentionEntriesSkipsIgnoredDirs(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "main.go"), "package main")
	if err := os.MkdirAll(filepath.Join(root, "src", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "src", "pkg", "a.go"), "package pkg")
	for _, d := range []string{".git", "node_modules", "__pycache__", ".stubbs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(root, d, "junk"), "x")
	}

	entries := scanMentionEntries(root)
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Path] = e.IsDir
	}
	if dir, ok := got["main.go"]; !ok || dir {
		t.Errorf("main.go missing or marked as dir: %+v", got)
	}
	if dir, ok := got["src"]; !ok || !dir {
		t.Errorf("src directory missing or not a dir: %+v", got)
	}
	if _, ok := got["src/pkg/a.go"]; !ok {
		t.Errorf("nested file missing: %+v", got)
	}
	for _, d := range []string{".git", "node_modules", "__pycache__", ".stubbs"} {
		for p := range got {
			if p == d || strings.HasPrefix(p, d+"/") {
				t.Errorf("ignored dir %q leaked into mentions: %q", d, p)
			}
		}
	}
}

func TestFuzzyScoreRanksBasenamePrefix(t *testing.T) {
	all := []mentionEntry{
		{Path: "vendor/other/readme.md"},
		{Path: "src/readme.go"},
		{Path: "readme.md"},
	}
	matches := filterMentionEntries(all, "readme")
	if len(matches) != 3 {
		t.Fatalf("got %d matches, want 3", len(matches))
	}
	// The shallowest basename match should win.
	if matches[0].Path != "readme.md" {
		t.Errorf("top match = %q, want readme.md", matches[0].Path)
	}
}

func TestFilterMentionSubsequence(t *testing.T) {
	all := []mentionEntry{{Path: "src/agent/interactive.go"}, {Path: "README.md"}}
	matches := filterMentionEntries(all, "sai")
	if len(matches) != 1 || matches[0].Path != "src/agent/interactive.go" {
		t.Fatalf("subsequence filter = %+v", matches)
	}
	if got := filterMentionEntries(all, "zzz"); len(got) != 0 {
		t.Errorf("no-match filter = %+v", got)
	}
}

func TestAtMentionBoundary(t *testing.T) {
	m := testModel()
	m.ta.SetWidth(80)
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"hello ", true},
		{"hello", false},
		{"foo@", false},
		{"(a)", true},
	}
	for _, tc := range cases {
		m.ta.SetValue(tc.value)
		m.ta.CursorEnd()
		if got := m.atMentionBoundary(); got != tc.want {
			t.Errorf("atMentionBoundary(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestTypingAtOpensMentionPicker(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	mustWrite(t, filepath.Join(m.workdir, "hello.txt"), "hi")
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})
	if m.mention == nil || !m.mention.active {
		t.Fatalf("typing @ should open the mention picker")
	}
	if m.ta.Value() != "@" {
		t.Errorf("composer = %q, want @", m.ta.Value())
	}
	if len(m.mention.matches) == 0 {
		t.Error("picker should show entries after scanning the workdir")
	}
}

func TestMentionQueryFiltersAndAccept(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	mustWrite(t, filepath.Join(m.workdir, "alpha.go"), "package alpha")
	mustWrite(t, filepath.Join(m.workdir, "beta.go"), "package beta")
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})
	for _, r := range "alpha" {
		_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.mention.query != "alpha" {
		t.Fatalf("query = %q, want alpha", m.mention.query)
	}
	if len(m.mention.matches) != 1 || m.mention.matches[0].Path != "alpha.go" {
		t.Fatalf("matches = %+v, want alpha.go", m.mention.matches)
	}
	// Enter accepts the highlighted entry and closes the popup.
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mention.active {
		t.Error("enter should close the picker")
	}
	if got := m.ta.Value(); got != "@alpha.go " {
		t.Errorf("composer = %q, want %q", got, "@alpha.go ")
	}
	// Submitting expands the mention into attached content.
	cmd := m.submitPending()
	_ = cmd
	res := <-reply
	if !strings.Contains(res.text, "package alpha") {
		t.Errorf("submitted text should embed file content, got:\n%s", res.text)
	}
}

func TestMentionEscapeKeepsAtToken(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mention.active {
		t.Error("esc should close the picker")
	}
	if m.ta.Value() != "@" {
		t.Errorf("composer = %q, want the bare @ preserved", m.ta.Value())
	}
}

func TestMentionRespectsBoundary(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}

	for _, r := range "foo" {
		_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})
	if m.mention != nil && m.mention.active {
		t.Error("@ in the middle of a word must not open the picker")
	}
	if m.ta.Value() != "foo@" {
		t.Errorf("composer = %q, want foo@", m.ta.Value())
	}
}

func TestExpandMentionsDirectory(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	mustWrite(t, filepath.Join(m.workdir, "sub", "x.go"), "package sub")

	out := m.expandMentions("look at @sub please")
	if !strings.Contains(out, "### @sub/") {
		t.Errorf("directory attachment missing:\n%s", out)
	}
	if !strings.Contains(out, "x.go") {
		t.Errorf("directory listing missing x.go:\n%s", out)
	}
}

func TestExpandMentionsSkipsMissingAndSecrets(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	mustWrite(t, filepath.Join(m.workdir, ".env"), "SECRET=1")

	if out := m.expandMentions("see @nope.txt"); strings.Contains(out, "attached content") {
		t.Errorf("missing path should not attach anything: %q", out)
	}
	out := m.expandMentions("read @.env")
	if strings.Contains(out, "SECRET=1") {
		t.Errorf("secret content must not be inlined by default: %q", out)
	}
	if !strings.Contains(out, "secrets is disabled") {
		t.Errorf("expected a warning about the skipped secret: %q", out)
	}
	m.readSecrets = true
	if out := m.expandMentions("read @.env"); !strings.Contains(out, "SECRET=1") {
		t.Errorf("readSecrets should inline secret files: %q", out)
	}
}

func TestExpandMentionsConfinesToWorkdir(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	out := m.expandMentions("read @../etc/passwd")
	if strings.Contains(out, "attached content") {
		t.Errorf("path escaping the workdir must not be attached: %q", out)
	}
}

func TestMentionPickerView(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	mustWrite(t, filepath.Join(m.workdir, "thing.go"), "package thing")
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})

	view := m.mentionView()
	if !strings.Contains(view, "thing.go") {
		t.Errorf("picker view missing entry:\n%s", view)
	}
	if !strings.Contains(view, "@ files") {
		t.Errorf("picker view missing title:\n%s", view)
	}
}

func TestMentionSpaceClosesPicker(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inTask, reply: reply}

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if m.mention.active {
		t.Error("space should close the picker")
	}
	if m.ta.Value() != "@ " {
		t.Errorf("composer = %q, want %q", m.ta.Value(), "@ ")
	}
}

func TestMentionDisabledForLimits(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	reply := make(chan inputResult, 1)
	m.pending = &pendingInput{kind: inLimits, reply: reply}
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})
	if m.mention != nil && m.mention.active {
		t.Error("the limits form must not open the mention picker")
	}
}

func TestExpandMentionsIgnoresEmail(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	out := m.expandMentions("mail me at user@example.com")
	if strings.Contains(out, "attached content") {
		t.Errorf("an email address must not be treated as a mention: %q", out)
	}
}

func TestExpandMentionsStripsTrailingPunctuation(t *testing.T) {
	m := testModel()
	m.workdir = t.TempDir()
	mustWrite(t, filepath.Join(m.workdir, "main.go"), "package main")
	out := m.expandMentions("see @main.go.")
	if !strings.Contains(out, "package main") {
		t.Errorf("trailing punctuation should be stripped from the path:\n%s", out)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
