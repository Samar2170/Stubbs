package tui

import (
	"fmt"
	"sort"
	"strings"
)

// dialogKind selects which bottom-sheet modal is active.
type dialogKind int

const (
	dlgHelp dialogKind = iota
	dlgConfirm
	dlgExit
	dlgModels
)

// dlgAction describes what picking an option does.
type dlgAction int

const (
	dlgResolve dlgAction = iota // resolve the pending prompt with text
	dlgReject                   // swap to a reject-comment composer
	dlgNewTask                  // swap to a new-task composer
)

type dlgOption struct {
	label  string
	text   string
	action dlgAction
}

type dialog struct {
	kind     dialogKind
	commands []string         // shown in the confirm dialog
	reply    chan inputResult // nil for the help overlay
	options  []dlgOption
	all      []dlgOption // unfiltered options (models picker)
	filter   string
	selected int
}

const maxModelRows = 12

func newModelsDialog(models []ModelChoice) *dialog {
	opts := make([]dlgOption, 0, len(models))
	for _, m := range models {
		label := m.Name
		if label == "" {
			label = m.ID
		} else if label != m.ID {
			label = fmt.Sprintf("%s · %s", label, m.ID)
		}
		opts = append(opts, dlgOption{label: label, text: m.ID})
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].text < opts[j].text })
	return &dialog{kind: dlgModels, all: opts, options: opts}
}

// applyFilter narrows the models list to the current filter string.
func (d *dialog) applyFilter() {
	f := strings.ToLower(strings.TrimSpace(d.filter))
	if f == "" {
		d.options = d.all
	} else {
		out := make([]dlgOption, 0, len(d.all))
		for _, o := range d.all {
			if strings.Contains(strings.ToLower(o.label), f) || strings.Contains(strings.ToLower(o.text), f) {
				out = append(out, o)
			}
		}
		d.options = out
	}
	if d.selected >= len(d.options) {
		d.selected = 0
	}
}

// visibleOptions returns the slice of options to draw and its offset. The
// models picker is windowed around the selection so long catalogs stay usable.
func (d *dialog) visibleOptions() ([]dlgOption, int) {
	if d.kind != dlgModels || len(d.options) <= maxModelRows {
		return d.options, 0
	}
	off := d.selected - maxModelRows/2
	if off < 0 {
		off = 0
	}
	if off > len(d.options)-maxModelRows {
		off = len(d.options) - maxModelRows
	}
	return d.options[off : off+maxModelRows], off
}

func confirmOptions() []dlgOption {
	return []dlgOption{
		{label: "Approve and run", text: ""},
		{label: "Approve all — switch to yolo mode", text: "/y"},
		{label: "Reject with a comment", action: dlgReject},
		{label: "Switch to human mode", text: "/u"},
	}
}

func exitOptions() []dlgOption {
	return []dlgOption{
		{label: "Finish", text: ""},
		{label: "New task", action: dlgNewTask},
		{label: "Switch to human mode", text: "/u"},
	}
}

func newHelpDialog() *dialog {
	return &dialog{kind: dlgHelp}
}

// render draws the dialog box; the caller centers it horizontally.
func (d *dialog) render(width int, s styles) string {
	cw := min(max(width-8, 16), 66)
	var lines []string
	switch d.kind {
	case dlgHelp:
		lines = append(lines, s.agent.Render("keys"))
		lines = append(lines, helpRows(
			[2]string{"enter", "submit / select"},
			[2]string{"tab", "complete slash command"},
			[2]string{"ctrl+c", "interrupt · quit when done"},
			[2]string{"esc", "interrupt · cancel"},
			[2]string{"ctrl+e", "expand input box"},
			[2]string{"ctrl+o", "toggle tool output"},
			[2]string{"ctrl+j", "newline in input"},
			[2]string{"mouse", "select text · auto-copies on release"},
		)...)
		lines = append(lines, "", s.agent.Render("slash commands"))
		lines = append(lines, helpRows(
			[2]string{"/", "open the command menu"},
			[2]string{"/h", "this help"},
			[2]string{"/u /c /y", "human · confirm · yolo mode"},
			[2]string{"/models", "switch model"},
			[2]string{"/remember", "save a memory"},
			[2]string{"/forget", "delete memories"},
			[2]string{"/memory", "list memories"},
			[2]string{"/map", "regenerate repo map"},
			[2]string{"/m", "expand input box"},
			[2]string{"q", "end run (limits prompt)"},
		)...)
	case dlgConfirm:
		lines = append(lines, s.agent.Render("approve commands?"))
		for _, c := range d.commands {
			lines = append(lines, wrapAt(cw).Render(s.faint.Render("→ "+c)))
		}
		lines = append(lines, "")
	case dlgModels:
		lines = append(lines, s.agent.Render("select model"))
		if d.filter != "" {
			lines = append(lines, s.info.Render("filter: "+d.filter))
		} else {
			lines = append(lines, s.faint.Render("type to filter"))
		}
		lines = append(lines, "")
	default: // exit
		lines = append(lines, s.agent.Render("agent wants to finish"), "")
	}
	opts, offset := d.visibleOptions()
	if d.kind == dlgModels && len(d.options) == 0 {
		lines = append(lines, s.faint.Render("  no matches"))
	}
	for i, o := range opts {
		if offset+i == d.selected {
			lines = append(lines, s.sel.Render("❯ "+o.label))
		} else {
			lines = append(lines, s.option.Render("  "+o.label))
		}
	}
	if d.kind == dlgModels {
		lines = append(lines, "", s.faint.Render(fmt.Sprintf("%d/%d", min(d.selected+1, len(d.options)), len(d.options))))
		lines = append(lines, s.faint.Render("↑/↓ move · type to filter · enter select · esc cancel"))
		return s.dialog.Render(wrapAt(cw).Render(strings.Join(lines, "\n")))
	}
	lines = append(lines, "", s.faint.Render("↑/↓ move · enter select · esc cancel"))
	return s.dialog.Render(wrapAt(cw).Render(strings.Join(lines, "\n")))
}

func helpRows(rows ...[2]string) []string {
	const col = 10
	out := make([]string, len(rows))
	for i, r := range rows {
		key := r[0]
		if pad := col - len(key); pad > 0 {
			key += strings.Repeat(" ", pad)
		}
		out[i] = key + r[1]
	}
	return out
}
