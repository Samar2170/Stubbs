package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"stubbs/src/types"
)

const (
	defaultSearchMaxMatches = 200
	defaultSearchMaxLineLen = 400
)

// searchSkipDirs are directory names never descended into during a search.
var searchSkipDirs = map[string]bool{
	".git": true, ".stubbs": true, "node_modules": true, "vendor": true,
	"__pycache__": true, ".venv": true, "venv": true,
}

// SearchTool greps file contents for a regular expression. It exists so the
// model can locate code in one call instead of many tiny bash sed/grep calls,
// which keeps transcripts (and per-turn latency) small.
type SearchTool struct {
	fileTool
	MaxMatches int
}

func NewSearchTool(root string) *SearchTool {
	return &SearchTool{
		fileTool:   newFileTool(root),
		MaxMatches: defaultSearchMaxMatches,
	}
}

func (t *SearchTool) Name() string { return "grep" }

func (t *SearchTool) Description() string {
	return "Search file contents with a regular expression and return matching " +
		"file paths and lines. Confined to the working directory. Prefer this " +
		"over reading whole files or shelling out to grep; narrow with 'include' " +
		"(e.g. \"*.go\") and 'path' when you can."
}

func (t *SearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"pattern": {
			"type": "string",
			"description": "regular expression to search for (RE2 syntax)"
		},
		"path": {
			"type": "string",
			"description": "file or directory to search, relative to the working directory (default \".\")"
		},
		"include": {
			"type": "string",
			"description": "optional glob to filter files by name, e.g. \"*.go\""
		},
		"ignore_case": {
			"type": "boolean",
			"description": "case-insensitive match"
		},
		"max_results": {
			"type": "integer",
			"description": "maximum number of matching lines to return"
		}
	},
	"required": ["pattern"]
}`)
}

type searchArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Include    string `json:"include"`
	IgnoreCase bool   `json:"ignore_case"`
	MaxResults int    `json:"max_results"`
}

func (t *SearchTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	var a searchArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return errOutput(t.Name(), fmt.Errorf("invalid arguments: %w", err))
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return errOutput(t.Name(), fmt.Errorf("pattern cannot be empty"))
	}
	pattern := a.Pattern
	if a.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return errOutput(t.Name(), fmt.Errorf("bad pattern: %w", err))
	}
	target, err := t.resolveInRoot(a.Path)
	if err != nil {
		return errOutput(t.Name(), err)
	}
	max := a.MaxResults
	if max <= 0 || max > t.maxMatches() {
		max = t.maxMatches()
	}

	root := t.rootDir
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}

	var (
		matches []string
		files   int
		full    bool
	)
	emit := func(path string, line int, text string) bool {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		matches = append(matches, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(rel), line, truncateLine(text)))
		if len(matches) >= max {
			full = true
			return false
		}
		return true
	}

	info, err := os.Stat(target)
	if err != nil {
		return errOutput(t.Name(), fmt.Errorf("cannot access path: %w", err))
	}
	if info.IsDir() {
		err = filepath.WalkDir(target, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if p != target && searchSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if a.Include != "" {
				if ok, _ := filepath.Match(a.Include, d.Name()); !ok {
					return nil
				}
			}
			files++
			return searchFile(p, re, emit)
		})
		if err != nil && ctx.Err() != nil {
			return types.ExecutionOutput{Error: fmt.Sprintf("%s: %v", t.Name(), ctx.Err()), Code: -1}
		}
	} else {
		files = 1
		_ = searchFile(target, re, emit)
	}

	if len(matches) == 0 {
		return types.ExecutionOutput{Output: fmt.Sprintf("no matches for %q in %d file(s)", a.Pattern, files), Code: 0}
	}
	out := strings.Join(matches, "\n")
	if full {
		out += fmt.Sprintf("\n... [stopped at %d matches] ...", max)
	}
	return types.ExecutionOutput{Output: truncateOutput(out, defaultMaxOutputSize), Code: 0}
}

func (t *SearchTool) maxMatches() int {
	if t.MaxMatches > 0 {
		return t.MaxMatches
	}
	return defaultSearchMaxMatches
}

// searchFile scans one file line by line, invoking emit for each match. Binary
// files (containing a NUL byte in the first block) are skipped.
func searchFile(path string, re *regexp.Regexp, emit func(string, int, string) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if strings.IndexByte(text, 0) >= 0 {
			return nil
		}
		if re.MatchString(text) {
			if !emit(path, line, text) {
				return nil
			}
		}
	}
	return sc.Err()
}

func truncateLine(s string) string {
	s = strings.TrimRight(s, " \t\r")
	if len(s) > defaultSearchMaxLineLen {
		return s[:defaultSearchMaxLineLen] + "…"
	}
	return s
}
