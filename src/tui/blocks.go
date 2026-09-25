package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"stubbs/src/types"
)

// block is a structured transcript element. Blocks hold raw data and render
// themselves at the current width, so the transcript reflows correctly on
// resize instead of carrying pre-styled lines.
type block interface {
	render(width int, s styles) string
}

func wrapAt(width int) lipgloss.Style {
	return lipgloss.NewStyle().Width(max(width, 1))
}

// boxed wraps a block's content in a clean rounded border. The border color
// identifies the speaker at a glance: user cyan, agent purple, tool neutral.
func (s styles) boxed(content string, borderColor lipgloss.Color) string {
	w := max(s.lastWidth, 1)
	body := wrapAt(max(w-4, 1)).Render(content) // -4: border (2) + padding (2)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Render(body)
}

// userBlock renders the user's task or follow-up text.
type userBlock struct{ text string }

func (b userBlock) render(width int, s styles) string {
	s.lastWidth = width
	body := strings.TrimSpace(b.text)
	return s.boxed(body, s.pal.user)
}

// assistantBlock renders one agent step: header, prose, planned commands.
type assistantBlock struct {
	step    int
	cost    float32
	content string
	calls   []string
}

func (b assistantBlock) render(width int, s styles) string {
	s.lastWidth = width
	var out []string
	header := s.agent.Render("stubbs") +
		s.faint.Render(fmt.Sprintf(" · step %d · $%.4f", b.step, b.cost))
	out = append(out, header)
	if c := strings.TrimSpace(b.content); c != "" {
		out = append(out, "", c)
	}
	for _, call := range b.calls {
		out = append(out, s.user.Render("→ "+call))
	}
	return s.boxed(strings.Join(out, "\n"), s.pal.accent)
}

// toolBlock renders one command execution. Output and error are shown
// inline (clipped); ctrl+o collapses/expands all tool blocks.
type toolBlock struct {
	name     string
	cmd      string
	out      types.ExecutionOutput
	expanded bool
}

func (b toolBlock) render(width int, s styles) string {
	s.lastWidth = width
	var parts []string
	title := s.dot(b.out.Code == 0) + " " + s.tool.Render(b.name)
	if b.cmd != "" {
		title += s.faint.Render("  " + firstLine(b.cmd))
	}
	title += s.faint.Render(fmt.Sprintf("  exit %d · %s", b.out.Code, shortDur(b.out.Duration)))
	parts = append(parts, title)
	if b.expanded {
		if o := strings.TrimSpace(b.out.Output); o != "" {
			parts = append(parts, indent(clip(o, 12), s.faint))
		}
		if e := strings.TrimSpace(b.out.Error); e != "" {
			parts = append(parts, indent(clip(e, 5), s.errStyle))
		}
	}
	return s.boxed(strings.Join(parts, "\n"), s.pal.border)
}

// infoBlock renders neutral bullet info (mode switches, help fallback…).
type infoBlock struct{ text string }

func (b infoBlock) render(width int, s styles) string {
	s.lastWidth = width
	return wrapAt(width).Render(s.info.Render("● ") + s.dim.Render(strings.TrimSpace(b.text)))
}

// errorBlock renders run failures.
type errorBlock struct{ text string }

func (b errorBlock) render(width int, s styles) string {
	s.lastWidth = width
	return s.boxed(s.errStyle.Render("● ")+strings.TrimSpace(b.text), s.pal.err)
}

// okBlock renders successful run completion.
type okBlock struct{ text string }

func (b okBlock) render(width int, s styles) string {
	s.lastWidth = width
	return s.boxed(s.ok.Render("● ")+strings.TrimSpace(b.text), s.pal.success)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func shortDur(d time.Duration) string {
	d = d.Round(time.Millisecond)
	return d.String()
}

// indent prefixes every line with two spaces and applies the style.
func indent(s string, st lipgloss.Style) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + st.Render(l)
	}
	return strings.Join(lines, "\n")
}

// clip keeps the first head and last tail lines of s, hiding the middle,
// so the result is exactly maxLines long.
func clip(s string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= maxLines {
		return s
	}
	keep := maxLines - 1 // room for the hidden-lines marker
	head := keep * 2 / 3
	tail := keep - head
	hidden := len(lines) - head - tail
	out := append([]string{}, lines[:head]...)
	out = append(out, fmt.Sprintf("… [%d lines hidden — ctrl+o toggles tool output]", hidden))
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
}

// plainWidth is an ANSI-aware display width helper.
func plainWidth(s string) int { return ansi.StringWidth(s) }
