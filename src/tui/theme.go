package tui

import "github.com/charmbracelet/lipgloss"

// palette holds the raw color slots of a theme. Every slot is a lipgloss
// color: either a hex value (truecolor) or an ANSI 0-15 index ("system"
// theme), so both presets degrade gracefully on limited terminals.
type palette struct {
	primary lipgloss.Color // brand / wordmark
	accent  lipgloss.Color // assistant voice, active accents
	user    lipgloss.Color // user voice
	success lipgloss.Color
	warning lipgloss.Color
	err     lipgloss.Color
	muted   lipgloss.Color // secondary text
	faint   lipgloss.Color // hints, timestamps
	border  lipgloss.Color
	text    lipgloss.Color // dialog body text
	badgeFg lipgloss.Color // text on top of accent-colored badges
}

var tokyo = palette{
	primary: "#7aa2f7",
	accent:  "#bb9af7",
	user:    "#7dcfff",
	success: "#9ece6a",
	warning: "#e0af68",
	err:     "#f7768e",
	muted:   "#a9b1d6",
	faint:   "#565f89",
	border:  "#292e42",
	text:    "#c0caf5",
	badgeFg: "#1a1b26",
}

// system uses only ANSI 0-15 and inherits the user's terminal palette, the
// way opencode's "system" theme does.
var system = palette{
	primary: "4",
	accent:  "5",
	user:    "6",
	success: "2",
	warning: "3",
	err:     "1",
	muted:   "7",
	faint:   "8",
	border:  "8",
	text:    "7",
	badgeFg: "0",
}

func loadTheme(name string) palette {
	if name == "system" {
		return system
	}
	return tokyo
}

// styles bundles every lipgloss style the TUI uses, derived from one palette.
type styles struct {
	pal palette

	// lastWidth carries the render width from a block's render() call into
	// its boxed() helper (Go has no per-call locals across helpers).
	lastWidth int

	title    lipgloss.Style // "stubbs" wordmark badge
	dim      lipgloss.Style
	faint    lipgloss.Style
	agent    lipgloss.Style // assistant header
	user     lipgloss.Style // user voice / planned commands
	tool     lipgloss.Style // tool names, output
	info     lipgloss.Style
	ok       lipgloss.Style
	errStyle lipgloss.Style
	box      lipgloss.Style
	boxFocus lipgloss.Style
	dialog   lipgloss.Style
	option   lipgloss.Style // unselected dialog option
	sel      lipgloss.Style // selected dialog option
}

func newStyles(p palette) styles {
	fg := func(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	return styles{
		pal:      p,
		title:    lipgloss.NewStyle().Bold(true).Background(p.primary).Foreground(p.badgeFg).Padding(0, 1),
		dim:      fg(p.muted),
		faint:    fg(p.faint),
		agent:    fg(p.accent).Bold(true),
		user:     fg(p.user).Bold(true),
		tool:     fg(p.muted),
		info:     fg(p.warning),
		ok:       fg(p.success),
		errStyle: fg(p.err).Bold(true),
		box:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(p.border),
		boxFocus: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(p.accent),
		dialog:   lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(p.primary).Padding(0, 2),
		option:   fg(p.muted),
		sel:      fg(p.badgeFg).Background(p.accent).Bold(true),
	}
}

// badge renders a mode/status pill.
func (s styles) badge(text string, bg lipgloss.Color) string {
	return lipgloss.NewStyle().Bold(true).Background(bg).Foreground(s.pal.badgeFg).Padding(0, 1).Render(text)
}

// modeBadge styles the current agent mode chip in the header.
func (s styles) modeBadge(mode string) string {
	switch mode {
	case "human":
		return s.badge("human", s.pal.user)
	case "yolo":
		return s.badge("yolo", s.pal.err)
	default:
		return s.badge("confirm", s.pal.warning)
	}
}

// dot renders a status bullet; green on success, red otherwise.
func (s styles) dot(ok bool) string {
	if ok {
		return s.ok.Render("●")
	}
	return s.errStyle.Render("●")
}
