package tui

import (
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// textareaRows reports how many display rows the composer's textarea will use
// for value at the given content width.
//
// The composer is sized by measuring its own content instead of relying on the
// textarea to scroll. A hard-wrap estimate (ceil(width/columns)) is not enough:
// bubbles/textarea word-wraps, and it also wraps when a line exactly fills the
// width, so the real row count can be higher. One row short means the textarea
// scrolls and the first line disappears from view. This mirrors the unexported
// wrap() in bubbles/textarea v1.0.0 (pinned in go.mod); wrap_test.go cross-checks
// it against textarea.LineInfo().Height so a dependency bump is caught.
func textareaRows(value string, width int) int {
	if width < 1 {
		width = 1
	}
	rows := 0
	for _, line := range strings.Split(value, "\n") {
		rows += len(textareaWrap([]rune(line), width))
	}
	if rows < 1 {
		return 1
	}
	return rows
}

// textareaWrap is a copy of bubbles/textarea's wrap(): word-wrap by rune
// display width, then hard-wrap tokens that do not fit on a line by themselves.
func textareaWrap(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
			}
			spaces = 0
			word = nil
		} else {
			// A double-width rune may not fit on the current line.
			lastCharLen := runewidth.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], repeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], repeatSpaces(spaces)...)
	}

	return lines
}

func repeatSpaces(n int) []rune {
	return []rune(strings.Repeat(" ", n))
}
