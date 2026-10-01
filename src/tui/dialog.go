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

	// rows is the row budget the dialog may occupy (0 = unbounded). The
	// model sets it from the terminal height so a long approval cannot push
	// the transcript off screen; the confirm dialog then windows its command
	// text instead of growing without limit.
	rows int

	// scroll is the first visible command row in the confirm dialog, and
	// region/total are the last rendered window geometry, so the key handler
	// can page and clamp without re-wrapping the commands.
	scroll int
	region int
	total  int
}

// maxModelRows caps the models picker; maxConfirmCmdRows caps the confirm
// dialog's command viewport. Both are further trimmed by the row budget.
const (
	maxModelRows      = 12
	maxConfirmCmdRows = 8
	// confirmScrollbarGutter is the width reserved on the right of every
	// command row for the scrollbar column, so drawing the bar never pushes
	// a wrapped command past the sheet width.
	confirmScrollbarGutter = 2
)

// resize records the row budget assigned by the model.
func (d *dialog) resize(rows int) { d.rows = rows }

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
	confirmBlank, confirmHint := true, true
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
		// Wrap every command first: the window counts display rows, not
		// commands, so a single command with a lot of text still pages.
		rows := d.wrapCommands(cw-confirmScrollbarGutter, s)
		fixed := 1 + d.optionRows(cw) + 2 // title + options + border
		window, footer, hint, blank := d.confirmWindow(len(rows), fixed)
		start := min(max(d.scroll, 0), max(len(rows)-window, 0))
		d.total, d.region, d.scroll = len(rows), window, start
		confirmBlank, confirmHint = blank, hint
		bar := d.scrollbar(start, window)
		for i := 0; i < window; i++ {
			lines = append(lines, rows[start+i]+bar[i])
		}
		if footer {
			lines = append(lines, s.faint.Render(d.confirmFooter(start)))
		}
		if blank {
			lines = append(lines, "")
		}
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
	if confirmBlank {
		lines = append(lines, "")
	}
	if confirmHint {
		lines = append(lines, s.faint.Render("↑/↓ move · enter select · esc cancel"))
	}
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

// optionRows is the number of display rows the option list occupies at the
// given width. Labels can wrap on narrow terminals, so the confirm budget must
// count actual rows rather than assume one per option.
func (d *dialog) optionRows(width int) int {
	total := 0
	for _, o := range d.options {
		total += len(strings.Split(wrapAt(width).Render("  "+o.label), "\n"))
	}
	return total
}

// wrapCommands renders every command to its own display rows at the given
// width. The window counts display rows rather than commands, which is what
// lets a single command with a lot of text page instead of growing the sheet.
func (d *dialog) wrapCommands(width int, s styles) []string {
	out := make([]string, 0, len(d.commands))
	for _, c := range d.commands {
		out = append(out, strings.Split(wrapAt(width).Render(s.faint.Render("→ "+c)), "\n")...)
	}
	return out
}

// confirmWindow decides how many command display rows to show for the current
// row budget and whether the optional footer, blank separators and hint fit.
// The optional chrome is dropped before the command window is starved, so the
// sheet never renders taller than the budget the model assigned. It keeps at
// least one command row, so the sheet always shows the command being approved.
func (d *dialog) confirmWindow(total, fixed int) (window int, footer, hint, blank bool) {
	if total == 0 {
		return 0, false, true, true
	}
	avail := maxConfirmCmdRows
	if d.rows > 0 {
		avail = d.rows - fixed
	}
	if avail < 1 {
		// Even one command row does not fit the budget (the terminal is too
		// small for the sheet); show one anyway and shed all optional chrome.
		return 1, false, false, false
	}
	window = min(avail, maxConfirmCmdRows)
	if total < window {
		window = total
	}
	// footer + two blank separators + hint are four optional rows; keep them
	// only when the leftover budget can actually hold them.
	if avail-window >= 4 {
		footer, hint, blank = total > window, true, true
	}
	return window, footer, hint, blank
}

// scrollbar draws a scrollbar column alongside a window of n rows: a filled
// thumb whose position and length track the visible region, and a track
// elsewhere. It is a blank column when the whole list fits, so short
// approvals keep their old look.
func (d *dialog) scrollbar(start, n int) []string {
	out := make([]string, max(n, 0))
	if n <= 0 || d.total <= n {
		return out
	}
	thumb := max(n*n/d.total, 1)
	trackTop := n - thumb
	pos := 0
	if trackTop > 0 {
		pos = (start * trackTop) / max(d.total-n, 1)
		pos = min(max(pos, 0), trackTop)
	}
	chars := []rune(strings.Repeat("│", n))
	for i := 0; i < thumb && pos+i < n; i++ {
		chars[pos+i] = '█'
	}
	for i, r := range chars {
		out[i] = " " + string(r)
	}
	return out
}

// confirmScrollable reports whether the confirm dialog has a command window
// to scroll. render() records the geometry (total/region), so probe with a
// throwaway render at the real width when the first key arrives before the
// dialog has been drawn with a styles value.
func (d *dialog) confirmScrollable(width int) bool {
	if d.kind != dlgConfirm {
		return false
	}
	if d.total == 0 && d.region == 0 {
		d.render(max(width, 1), styles{})
	}
	return d.total > d.region && d.region > 0
}

// pageCommands advances the scroll offset by delta rows (floored at the top)
// and re-renders so region/total are up to date for the next clamp.
func (d *dialog) pageCommands(delta, width int, s styles) {
	d.scroll = max(d.scroll+delta, 0)
	d.render(width, s)
}

// confirmPage is the number of command rows a page key should advance: one
// screenful minus the overlap row that keeps context across the jump.
func (d *dialog) confirmPage() int {
	return max(d.region-1, 1)
}

// confirmFooter is the scroll hint shown under a windowed command list, with a
// percentage so the user can tell how far down the command they are.
func (d *dialog) confirmFooter(start int) string {
	denom := d.total - d.region
	if denom <= 0 {
		return "↑/↓ scroll"
	}
	pct := min(start*100/denom, 100)
	return fmt.Sprintf("↑/↓ scroll · %d%%", pct)
}
