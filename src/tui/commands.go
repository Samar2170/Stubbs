package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"stubbs/src/agent"
)

// slashCommand is one entry in the composer's slash-command menu.
type slashCommand struct {
	name     string // e.g. "/remember"
	args     string // argument hint, e.g. "<text>"
	desc     string // one-line description
	takesArg bool   // completing it leaves the cursor after a space
}

// display is the label shown in the menu, including the argument hint.
func (c slashCommand) display() string {
	if c.args == "" {
		return c.name
	}
	return c.name + " " + c.args
}

type menuMode int

const (
	menuCommands menuMode = iota
	menuFiles
)

// commandMenu is the composer's completion popup. It opens for a leading
// "/..." token (slash commands) or a trailing "@..." token (file context),
// filters as the user types, and closes as soon as they move on (a space,
// another word, an empty box).
type commandMenu struct {
	open     bool
	mode     menuMode
	query    string // the token being typed, e.g. "/rem" or "@main"
	options  []slashCommand
	selected int
}

const maxCommandRows = 8

// refresh recomputes the menu from the composer text. Selection resets only
// when the query changes, so moving the cursor (or a no-op update) keeps the
// highlighted row.
func (m *commandMenu) refresh(text string, all []slashCommand) {
	query := commandQuery(text)
	if query == "" {
		m.close()
		return
	}
	if query != m.query {
		m.selected = 0
	}
	m.mode = menuCommands
	m.query = query
	m.options = filterCommands(all, query)
	m.open = len(m.options) > 0
	if m.selected >= len(m.options) {
		m.selected = 0
	}
}

// refreshFiles recomputes the menu from a trailing @token. The file index is
// supplied by the caller so it can be cached across keystrokes.
func (m *commandMenu) refreshFiles(text string, files []string) {
	query, _, ok := mentionQuery(text)
	if !ok {
		m.close()
		return
	}
	if query != m.query {
		m.selected = 0
	}
	m.mode = menuFiles
	m.query = query
	m.options = filterFiles(files, query)
	m.open = len(m.options) > 0
	if m.selected >= len(m.options) {
		m.selected = 0
	}
}

func (m *commandMenu) close() {
	m.open = false
	m.mode = menuCommands
	m.query = ""
	m.options = nil
	m.selected = 0
}

// move cycles the highlighted row by delta (wrapping).
func (m *commandMenu) move(delta int) {
	n := len(m.options)
	if n == 0 {
		return
	}
	m.selected = (m.selected + delta + n) % n
}

// visible returns the rows to draw and the index the window starts at. Long
// menus are windowed around the selection so they stay usable.
func (m *commandMenu) visible(limit int) ([]slashCommand, int) {
	n := len(m.options)
	if n <= limit {
		return m.options, 0
	}
	off := m.selected - limit/2
	off = min(max(off, 0), n-limit)
	return m.options[off : off+limit], off
}

// height is the number of terminal rows the menu occupies (rows + border),
// or zero when it is hidden. It feeds the layout the same way the dialogs do.
func (m *model) menuHeight() int {
	if !m.menu.open {
		return 0
	}
	opts, _ := m.menu.visible(m.menuLimit())
	if len(opts) == 0 {
		return 0
	}
	return len(opts) + 2 // rounded border
}

// menuLimit caps the menu so it never eats the whole transcript viewport.
// layout() sets inputH and the row budgets before it asks for usedRows(),
// which is why they are already known here.
func (m *model) menuLimit() int {
	if m.h <= 0 {
		return maxCommandRows
	}
	// header + transcript row + composer border (2) + menu border (2) + the
	// rows already budgeted to the status line and pending prompt.
	room := m.h - m.inputH - 4 - max(m.headerH, 1) - max(m.statusRows, 1) - m.pendingRows
	return max(1, min(maxCommandRows, room))
}

// commandQuery returns the slash token currently being typed, or "" when the
// composer is not in command-selection mode. A space after the token means
// the user is typing arguments, so the menu steps aside.
func commandQuery(text string) string {
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	if strings.ContainsAny(text, " \t\n") {
		return ""
	}
	return text
}

// filterCommands narrows the catalog to commands whose name starts with the
// query (case-insensitive).
func filterCommands(all []slashCommand, query string) []slashCommand {
	q := strings.ToLower(query)
	out := make([]slashCommand, 0, len(all))
	for _, c := range all {
		if strings.HasPrefix(strings.ToLower(c.name), q) {
			out = append(out, c)
		}
	}
	return out
}

// commands is the catalog offered by the menu, pruned to what is actually
// wired up in this session.
func (m *model) commands() []slashCommand {
	cmds := []slashCommand{
		{name: "/h", desc: "show help"},
		{name: "/models", desc: "switch model"},
		{name: "/m", desc: "expand input box"},
		{name: "/u", desc: "human mode"},
		{name: "/c", desc: "confirm mode"},
		{name: "/y", desc: "yolo mode"},
	}
	if m.app != nil && m.app.sessions != nil && m.pending != nil && m.pending.kind == inTask {
		cmds = append(cmds, slashCommand{name: "/sessions", desc: "open a previous session"})
	}
	if m.app == nil || m.app.memory == nil {
		return cmds
	}
	cmds = append(cmds,
		slashCommand{name: "/remember", args: "<text>", desc: "save a memory", takesArg: true},
		slashCommand{name: "/forget", args: "<id|query>", desc: "delete memories", takesArg: true},
		slashCommand{name: "/memory", desc: "list memories"},
		slashCommand{name: "/map", desc: "regenerate repo map"},
	)
	return cmds
}

// syncMenu keeps the popup in step with the composer text. The menu only
// makes sense while a prompt is waiting for input: outside of one the composer
// cannot be submitted, so offering commands would be misleading. Safe to call
// after any composer mutation; it is pure bookkeeping and never re-lays out.
func (m *model) syncMenu() {
	if m.done || m.dlg != nil || m.pending == nil || m.pending.kind == inLimits {
		m.menu.close()
		return
	}
	text := m.ta.Value()
	if mentionPrompt(m.pending.kind) {
		if _, _, ok := mentionQuery(text); ok {
			m.menu.refreshFiles(text, m.fileCandidates())
			return
		}
	}
	m.menu.refresh(text, m.commands())
}

// fileCandidates returns the @-mention file index for the working directory.
// It is rebuilt when a fresh mention menu opens, not on every keystroke, so
// files created during a session still show up while typing stays responsive.
func (m *model) fileCandidates() []string {
	if !m.filesLoaded || m.menu.mode != menuFiles {
		root := m.opts.Workdir
		if root == "" {
			root = "."
		}
		m.files = agent.ListFiles(root)
		m.filesLoaded = true
	}
	return m.files
}

// acceptCommand completes the highlighted command into the composer. Arg-less
// commands submit right away when submit is true and a prompt is pending;
// commands that take arguments just insert "name " and wait for the user.
func (m *model) acceptCommand(submit bool) tea.Cmd {
	if !m.menu.open || len(m.menu.options) == 0 {
		return nil
	}
	c := m.menu.options[m.menu.selected]
	if m.menu.mode == menuFiles {
		m.acceptMention(c)
		return nil
	}
	text := c.name
	if c.takesArg {
		text += " "
	}
	m.ta.SetValue(text)
	m.ta.CursorEnd()
	m.syncMenu()
	m.layout()
	if submit && !c.takesArg && m.pending != nil {
		return m.submitPending()
	}
	return nil
}

// acceptMention replaces the trailing @token with the selected path and adds a
// space so the user can keep typing. It never submits the prompt.
func (m *model) acceptMention(c slashCommand) {
	text := m.ta.Value()
	_, start, ok := mentionQuery(text)
	if !ok {
		return
	}
	m.ta.SetValue(text[:start] + c.name + " ")
	m.ta.CursorEnd()
	m.syncMenu()
	m.layout()
}

// renderMenu draws the popup box, aligned with the composer. The caller places
// it directly above the input box. The border matches the composer's width so
// the two stack cleanly.
func (m *model) renderMenu() string {
	opts, off := m.menu.visible(m.menuLimit())
	if len(opts) == 0 {
		return ""
	}
	// Border (2) + horizontal padding (2) keeps the popup as wide as the
	// composer's box below it.
	contentW := max(m.w-4, 8)
	nameW := 0
	for _, c := range opts {
		nameW = max(nameW, plainWidth(c.display()))
	}
	// Leave room for the cursor column, the gap and a few description chars.
	nameW = min(nameW, max(contentW-8, 4))
	rows := make([]string, 0, len(opts))
	for i, c := range opts {
		label := ansi.Truncate(c.display(), nameW, "…")
		label += strings.Repeat(" ", max(nameW-plainWidth(label), 0))
		prefix := label + "  "
		desc := ansi.Truncate(c.desc, max(contentW-plainWidth(prefix)-2, 1), "…")
		pos := "  "
		if off+i == m.menu.selected {
			pos = "❯ "
		}
		line := pos + prefix + desc
		line += strings.Repeat(" ", max(contentW-plainWidth(line), 0))
		if off+i == m.menu.selected {
			rows = append(rows, m.st.sel.Render(line))
			continue
		}
		rows = append(rows, m.st.cmdName.Render(line))
	}
	return m.st.menu.Render(strings.Join(rows, "\n"))
}
