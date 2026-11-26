package tui

import (
	"strings"
)

// mentionQuery inspects the token under the cursor for @-file completion.
// Only the last token of the composer is considered, which is where the user
// is typing; a token must start with @ and contain no whitespace.
func mentionQuery(text string) (query string, start int, ok bool) {
	start = strings.LastIndexAny(text, " \t\n") + 1
	token := text[start:]
	if !strings.HasPrefix(token, "@") {
		return "", 0, false
	}
	return token[1:], start, true
}

// mentionPrompt reports whether the active prompt accepts file context. Shell
// commands must reach bash verbatim, so only model-directed prompts qualify.
func mentionPrompt(kind inputKind) bool {
	switch kind {
	case inTask, inComment, inReject:
		return true
	}
	return false
}

// filterFiles narrows the file index to paths whose relative path or base
// name starts with the query (case-insensitive).
func filterFiles(files []string, query string) []slashCommand {
	q := strings.ToLower(query)
	out := make([]slashCommand, 0, len(files))
	for _, f := range files {
		if !fileMatches(f, q) {
			continue
		}
		out = append(out, slashCommand{name: "@" + f, desc: "file", takesArg: true})
	}
	return out
}

func fileMatches(path, query string) bool {
	if query == "" {
		return true
	}
	lower := strings.ToLower(path)
	if strings.HasPrefix(lower, query) {
		return true
	}
	base := lower
	if i := strings.LastIndexByte(lower, '/'); i >= 0 {
		base = lower[i+1:]
	}
	return strings.HasPrefix(base, query)
}
