package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// mentionEntry is one selectable file or directory in the @ picker.
type mentionEntry struct {
	Path  string
	IsDir bool
}

// mention tracks the @-triggered file picker. The popup appears in the
// composer when the user types "@" at a word boundary; the characters typed
// afterwards become the filter query. Accepting an entry replaces the token
// with "@<path> " in the input.
type mention struct {
	active bool
	query  string

	all      []mentionEntry // every scanned entry (shared, not filtered)
	matches  []mentionEntry // current filtered result
	selected int
}

const (
	// maxMentionEntries bounds how much of the tree we materialise, so a
	// mention in a huge working directory cannot blow up memory.
	maxMentionEntries = 20000
	// maxMentionMatches bounds the filtered list; the popup only draws a
	// window of it anyway.
	maxMentionMatches = 500
	// maxMentionRows is how many entries the popup shows at once.
	maxMentionRows = 8

	// maxMentionFileBytes caps how much of one attached file is inlined.
	maxMentionFileBytes = 48 << 10
	// maxMentionDirEntries caps how many entries a directory attachment lists.
	maxMentionDirEntries = 500
)

// mentionToken matches an @path token in a submitted prompt. Only an "@" at
// the start of the input or after whitespace counts, so email addresses like
// user@example.com are not mistaken for file references. The quoted form
// allows paths containing spaces.
var mentionToken = regexp.MustCompile(`(?:^|\s)@(?:"([^"]+)"|(\S+))`)

// mentionTrailingPunct is stripped from the end of an unquoted mention so
// punctuation at the end of a sentence ("see @main.go.") does not leak into
// the path.
const mentionTrailingPunct = `.,;:!?)]}`

// resolveMentionPath joins a mention path onto the working directory and
// confines it there, mirroring the file tools' sandbox.
func (m *model) resolveMentionPath(p string) (string, bool) {
	root := m.workdir
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	target := p
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	rel, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

// expandMentions rewrites a submitted prompt, appending the contents of each
// referenced file and a listing for each referenced directory. Unresolvable
// or secret paths are left in place so the agent still sees the reference.
func (m *model) expandMentions(text string) string {
	matches := mentionToken.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	seen := map[string]bool{}
	var attachments []string
	var warns []string
	for _, idx := range matches {
		raw := ""
		if idx[2] >= 0 {
			raw = text[idx[2]:idx[3]]
		} else if idx[4] >= 0 {
			raw = text[idx[4]:idx[5]]
			// Strip trailing sentence punctuation from bare paths.
			raw = strings.TrimRight(raw, mentionTrailingPunct)
		}
		raw = strings.TrimSuffix(raw, "/")
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		path, ok := m.resolveMentionPath(raw)
		if !ok {
			continue
		}
		if !m.readSecrets && isSecretPath(path) {
			warns = append(warns, fmt.Sprintf("skipped %s (reading secrets is disabled)", raw))
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			attachments = append(attachments, dirAttachment(raw, path))
		} else {
			if a, err := fileAttachment(raw, path); err == nil {
				attachments = append(attachments, a)
			} else {
				warns = append(warns, err.Error())
			}
		}
	}
	if len(attachments) == 0 && len(warns) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	if len(attachments) > 0 {
		b.WriteString("\n\n--- attached content ---")
		for _, a := range attachments {
			b.WriteString("\n\n")
			b.WriteString(a)
		}
	}
	for _, w := range warns {
		b.WriteString("\n\n[")
		b.WriteString(w)
		b.WriteString("]")
	}
	return b.String()
}

func fileAttachment(rel, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("could not attach %s: %v", rel, err)
	}
	content := string(data)
	if len(content) > maxMentionFileBytes {
		content = content[:maxMentionFileBytes] + "\n… [truncated]"
	}
	lang := strings.TrimPrefix(filepath.Ext(path), ".")
	return fmt.Sprintf("### @%s\n```%s\n%s\n```", rel, lang, content), nil
}

func dirAttachment(rel, path string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### @%s/", rel)
	entries, err := os.ReadDir(path)
	if err != nil {
		fmt.Fprintf(&b, "\n(could not list directory: %v)", err)
		return b.String()
	}
	count := 0
	for _, e := range entries {
		if count >= maxMentionDirEntries {
			b.WriteString("\n… [truncated]")
			break
		}
		if e.IsDir() {
			fmt.Fprintf(&b, "\n%s/", e.Name())
		} else {
			info, ierr := e.Info()
			if ierr != nil {
				fmt.Fprintf(&b, "\n%s", e.Name())
			} else {
				fmt.Fprintf(&b, "\n%s\t%d", e.Name(), info.Size())
			}
		}
		count++
	}
	return b.String()
}

// isSecretPath mirrors the file tools' .env detection.
func isSecretPath(path string) bool {
	base := filepath.Base(path)
	return base == ".env" || strings.HasPrefix(base, ".env.")
}

// ignoredMentionDirs are directories that are never worth offering as
// mentions: VCS metadata, stubbs' own state, and dependency caches.
var ignoredMentionDirs = map[string]bool{
	".git":         true,
	".stubbs":      true,
	"node_modules": true,
	"__pycache__":  true,
	".venv":        true,
}

// scanMentionEntries walks root and records every file and directory as a
// working-directory-relative slash path.
func scanMentionEntries(root string) []mentionEntry {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	entries := make([]mentionEntry, 0, 256)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		if d.IsDir() && ignoredMentionDirs[d.Name()] {
			return filepath.SkipDir
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		entries = append(entries, mentionEntry{
			Path:  filepath.ToSlash(rel),
			IsDir: d.IsDir(),
		})
		if len(entries) >= maxMentionEntries {
			return filepath.SkipAll
		}
		return nil
	})
	return entries
}

// filterMentionEntries returns the entries matching query, best first. An
// empty query lists shallow entries first so the popup opens on the most
// browsable part of the tree.
func filterMentionEntries(all []mentionEntry, query string) []mentionEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		out := make([]mentionEntry, len(all))
		copy(out, all)
		sort.SliceStable(out, func(i, j int) bool {
			di, dj := strings.Count(out[i].Path, "/"), strings.Count(out[j].Path, "/")
			if di != dj {
				return di < dj
			}
			if out[i].IsDir != out[j].IsDir {
				return out[i].IsDir
			}
			return out[i].Path < out[j].Path
		})
		if len(out) > maxMentionMatches {
			out = out[:maxMentionMatches]
		}
		return out
	}

	type scored struct {
		entry mentionEntry
		score int
	}
	matches := make([]scored, 0, 32)
	for _, e := range all {
		if score, ok := fuzzyScore(e.Path, q); ok {
			matches = append(matches, scored{entry: e, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].entry.Path < matches[j].entry.Path
	})
	if len(matches) > maxMentionMatches {
		matches = matches[:maxMentionMatches]
	}
	out := make([]mentionEntry, len(matches))
	for i, m := range matches {
		out[i] = m.entry
	}
	return out
}

// fuzzyScore ranks s against the lower-case query q. Exact substring hits
// score highest, with a bonus for matching the basename; otherwise a
// subsequence match is accepted.
func fuzzyScore(s, q string) (int, bool) {
	if q == "" {
		return 0, true
	}
	sr := []rune(s)
	lr := make([]rune, len(sr))
	for i, r := range sr {
		lr[i] = unicode.ToLower(r)
	}
	qr := []rune(q)
	ls := string(lr)

	if idx := strings.Index(ls, q); idx >= 0 {
		score := 500 - idx
		if base := strings.ToLower(filepath.Base(s)); strings.HasPrefix(base, q) {
			score += 300
			if base == q {
				score += 200
			}
		}
		if filepath.Base(s) == s {
			score += 50 // top-level entries beat nested ones
		}
		return score, true
	}

	// Subsequence match: every query rune must appear in order.
	li, score, prev := 0, 0, -2
	for _, r := range qr {
		found := -1
		for j := li; j < len(lr); j++ {
			if lr[j] == r {
				found = j
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		if found == prev+1 {
			score += 15
		} else {
			score += 5
		}
		prev = found
		li = found + 1
	}
	score -= len(sr) / 4
	return score, true
}

// canMention reports whether the composer is in a state where file mentions
// make sense: a conversation prompt (task, comment or rejection), not a
// human-mode command or the limits form, and not while a modal is open.
func (m *model) canMention() bool {
	if m.done || m.dlg != nil || m.pending == nil {
		return false
	}
	switch m.pending.kind {
	case inTask, inComment, inReject:
		return true
	default:
		return false
	}
}

// startMention opens the picker. The caller has already inserted the "@".
func (m *model) startMention() {
	m.ensureMentionEntries()
	m.mention = &mention{
		active:  true,
		all:     m.mentionCache,
		matches: filterMentionEntries(m.mentionCache, ""),
	}
	m.layout()
}

func (m *model) ensureMentionEntries() {
	if m.mentionLoaded {
		return
	}
	m.mentionCache = scanMentionEntries(m.workdir)
	m.mentionLoaded = true
}

func (m *model) closeMention() {
	if m.mention == nil || !m.mention.active {
		return
	}
	m.mention.active = false
	m.layout()
}

// cursorPos returns the composer's logical cursor position as (row, rune
// col). The textarea exposes the row and the soft-wrapped line info, from
// which the absolute column can be reconstructed.
func (m *model) cursorPos() (row, col int) {
	row = m.ta.Line()
	li := m.ta.LineInfo()
	col = li.StartColumn + li.ColumnOffset
	if col < 0 {
		col = 0
	}
	return row, col
}

// atMentionBoundary reports whether inserting "@" at the cursor starts a new
// token rather than landing in the middle of a word (e.g. an email address).
func (m *model) atMentionBoundary() bool {
	row, col := m.cursorPos()
	lines := strings.Split(m.ta.Value(), "\n")
	if row < 0 || row >= len(lines) {
		return true
	}
	runes := []rune(lines[row])
	if col <= 0 || col > len(runes) {
		return true
	}
	prev := runes[col-1]
	return !unicode.IsLetter(prev) && !unicode.IsDigit(prev) && prev != '@'
}

// moveMentionSelection moves the highlighted entry by delta, wrapping.
func (mn *mention) moveSelection(delta int) {
	n := len(mn.matches)
	if n == 0 {
		return
	}
	mn.selected = (mn.selected + delta + n) % n
}

// acceptMention replaces the "@query" token with "@<path> " and closes the
// popup. The query characters are deleted in place (leaving the "@") and the
// chosen path inserted, so the composer cursor follows naturally.
func (m *model) acceptMention() {
	mn := m.mention
	if mn == nil || !mn.active {
		return
	}
	if len(mn.matches) == 0 {
		m.closeMention()
		return
	}
	entry := mn.matches[mn.selected]
	path := entry.Path
	if entry.IsDir {
		path += "/"
	}
	token := path
	if strings.ContainsAny(path, " \t") {
		token = `"` + path + `"`
	}
	for range []rune(mn.query) {
		m.ta, _ = m.ta.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m.ta.InsertString(token + " ")
	m.closeMention()
	m.layout()
}

// handleMentionKey processes a key while the picker is open. It returns
// handled=false for keys the picker does not consume, after closing it, so
// normal composer/global handling can continue.
func (m *model) handleMentionKey(msg tea.KeyMsg) (handled bool, cmd tea.Cmd) {
	mn := m.mention
	if mn == nil || !mn.active {
		return false, nil
	}
	switch msg.String() {
	case "up", "ctrl+p":
		mn.moveSelection(-1)
		return true, nil
	case "down", "ctrl+n":
		mn.moveSelection(1)
		return true, nil
	case "tab":
		m.acceptMention()
		return true, nil
	case "enter":
		m.acceptMention()
		return true, nil
	case "esc", "ctrl+c":
		m.closeMention()
		return true, nil
	case "backspace":
		if mn.query == "" {
			// Nothing typed after "@": remove the "@" itself too.
			m.closeMention()
			return true, m.updateTA(msg)
		}
		runes := []rune(mn.query)
		mn.query = string(runes[:len(runes)-1])
		mn.matches = filterMentionEntries(mn.all, mn.query)
		mn.selected = 0
		return true, m.updateTA(msg)
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if unicode.IsSpace(r) {
			m.closeMention()
			return true, m.updateTA(msg)
		}
		mn.query += string(r)
		mn.matches = filterMentionEntries(mn.all, mn.query)
		mn.selected = 0
		return true, m.updateTA(msg)
	}
	m.closeMention()
	return false, nil
}

// mentionView renders the picker popup; the caller centers it. It returns an
// empty string when the picker is inactive.
func (m *model) mentionView() string {
	mn := m.mention
	if mn == nil || !mn.active {
		return ""
	}
	s := m.st
	cw := min(max(m.w-8, 16), 66)
	lines := []string{s.agent.Render("@ files")}
	if mn.query != "" {
		lines = append(lines, s.info.Render("@"+mn.query))
	} else {
		lines = append(lines, s.faint.Render("type to filter files and directories"))
	}

	if len(mn.matches) == 0 {
		lines = append(lines, s.faint.Render("  no matches"))
	}
	start := 0
	if len(mn.matches) > maxMentionRows {
		start = mn.selected - maxMentionRows/2
		if start < 0 {
			start = 0
		}
		if start > len(mn.matches)-maxMentionRows {
			start = len(mn.matches) - maxMentionRows
		}
	}
	end := min(start+maxMentionRows, len(mn.matches))
	for i := start; i < end; i++ {
		label := mn.matches[i].Path
		if mn.matches[i].IsDir {
			label += "/"
		}
		if i == mn.selected {
			lines = append(lines, s.sel.Render("❯ "+label))
		} else {
			lines = append(lines, s.option.Render("  "+label))
		}
	}
	if len(mn.matches) > 0 {
		lines = append(lines, "", s.faint.Render(
			fmt.Sprintf("%d/%d", mn.selected+1, len(mn.matches))))
	}
	lines = append(lines, s.faint.Render("↑/↓ move · enter/tab select · esc cancel"))
	return s.dialog.Render(wrapAt(cw).Render(strings.Join(lines, "\n")))
}
