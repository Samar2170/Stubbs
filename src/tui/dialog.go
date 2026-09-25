package tui

import (
	"strings"
)

// dialogKind selects which bottom-sheet modal is active.
type dialogKind int

const (
	dlgHelp dialogKind = iota
	dlgConfirm
	dlgExit
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
	selected int
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
			[2]string{"ctrl+c", "interrupt · quit when done"},
			[2]string{"esc", "interrupt · cancel"},
			[2]string{"ctrl+e", "expand input box"},
			[2]string{"ctrl+o", "toggle tool output"},
			[2]string{"ctrl+j", "newline in input"},
			[2]string{"mouse", "select text · auto-copies on release"},
		)...)
		lines = append(lines, "", s.agent.Render("slash commands"))
		lines = append(lines, helpRows(
			[2]string{"/h", "this help"},
			[2]string{"/u /c /y", "human · confirm · yolo mode"},
			[2]string{"/m", "expand input box"},
			[2]string{"q", "end run (limits prompt)"},
		)...)
	case dlgConfirm:
		lines = append(lines, s.agent.Render("approve commands?"))
		for _, c := range d.commands {
			lines = append(lines, wrapAt(cw).Render(s.faint.Render("→ "+c)))
		}
		lines = append(lines, "")
	default: // exit
		lines = append(lines, s.agent.Render("agent wants to finish"), "")
	}
	for i, o := range d.options {
		if i == d.selected {
			lines = append(lines, s.sel.Render("❯ "+o.label))
		} else {
			lines = append(lines, s.option.Render("  "+o.label))
		}
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
