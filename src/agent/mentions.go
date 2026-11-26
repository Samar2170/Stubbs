package agent

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	mentionMaxFileBytes  = 128 << 10
	mentionMaxTotalBytes = 512 << 10
	mentionMaxFiles      = 20000
)

// mentionPattern matches an @path token at the start of the text or after
// whitespace. The leading @ of an email address is never a mention.
var mentionPattern = regexp.MustCompile(`(^|\s)@([^\s@]+)`)

// mentionSkipDirs are directories that never contain mentionable files:
// VCS metadata, stubbs' own state, and dependency/build caches.
var mentionSkipDirs = map[string]bool{
	".git": true, ".stubbs": true, ".idea": true, ".vscode": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "out": true, "__pycache__": true, ".venv": true,
	"venv": true, "bench": true, ".cache": true,
}

// ListFiles returns the repository-relative, slash-separated paths of files
// under root, skipping the noise directories in mentionSkipDirs. The list
// feeds the composer's @-mention completion.
func ListFiles(root string) []string {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	paths := make([]string, 0, 128)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && mentionSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		if len(paths) >= mentionMaxFiles {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}

// ExpandFileMentions appends the contents of files referenced with @path to
// the prompt, so the model receives them as context without spending a tool
// call. Paths are resolved inside workdir; missing, oversized (beyond the
// budget), binary, and out-of-tree files are left alone. Repeated mentions of
// the same file add its contents once.
func ExpandFileMentions(text, workdir string) string {
	if !strings.Contains(text, "@") {
		return text
	}
	if strings.TrimSpace(workdir) == "" {
		workdir = "."
	}
	root, err := filepath.Abs(workdir)
	if err != nil {
		return text
	}
	matches := mentionPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}

	var block strings.Builder
	seen := make(map[string]bool)
	total := 0
	for _, match := range matches {
		if total >= mentionMaxTotalBytes {
			break
		}
		token := text[match[4]:match[5]]
		rel, data, ok := readMention(root, token)
		if !ok || seen[rel] {
			continue
		}
		seen[rel] = true
		if len(data) > mentionMaxFileBytes {
			data = data[:mentionMaxFileBytes]
		}
		if remaining := mentionMaxTotalBytes - total; len(data) > remaining {
			data = data[:remaining]
		}
		total += len(data)
		if block.Len() == 0 {
			block.WriteString("\n\nFiles the user attached with @:\n")
		}
		fmt.Fprintf(&block, "\n--- %s ---\n%s\n", rel, strings.TrimRight(string(data), "\n"))
	}
	if block.Len() == 0 {
		return text
	}
	return text + block.String()
}

// readMention resolves token inside root and returns the relative path and
// contents. It rejects directories, paths that escape root, and binary data.
func readMention(root, token string) (string, []byte, bool) {
	path := filepath.FromSlash(token)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil, false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", nil, false
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return "", nil, false
	}
	data, err := os.ReadFile(abs)
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return "", nil, false
	}
	return filepath.ToSlash(rel), data, true
}
