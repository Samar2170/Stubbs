package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"stubbs/src/types"
)

const (
	defaultFileReadMaxOutputSize = 16 << 10
	defaultReadLimitLines        = 2000
	defaultListMaxEntries        = 1000

	// ignoredDirName is stubbs' own session directory; it is never listed.
	ignoredDirName = ".stubbs"
)

// isSecretFile reports whether a path looks like an environment/secrets file.
// It matches ".env" and variants such as ".env.local" or ".env.production".
func isSecretFile(path string) bool {
	base := filepath.Base(path)
	return base == ".env" || strings.HasPrefix(base, ".env.")
}

// fileTool carries the root directory that every file tool is confined to.
type fileTool struct {
	rootDir string
}

func newFileTool(root string) fileTool {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	return fileTool{rootDir: root}
}

// resolveInRoot joins p onto the tool root and rejects anything that escapes
// it. Symlinks are followed on the deepest existing ancestor so a link inside
// the root cannot be used to reach outside it.
func (f fileTool) resolveInRoot(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		p = "."
	}
	root := f.rootDir
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}

	var target string
	if filepath.IsAbs(p) {
		target = filepath.Clean(p)
	} else {
		target = filepath.Join(root, p)
	}
	resolved := resolveExisting(target)

	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the working directory", p)
	}
	return resolved, nil
}

// resolveExisting follows symlinks on the deepest existing ancestor of p and
// re-appends the non-existent remainder, so paths that do not exist yet can
// still be validated.
func resolveExisting(p string) string {
	var remainder []string
	cur := filepath.Clean(p)
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(remainder) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, remainder[i])
			}
			return resolved
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(p)
		}
		remainder = append(remainder, filepath.Base(cur))
		cur = parent
	}
}

func errOutput(tool string, err error) types.ExecutionOutput {
	return types.ExecutionOutput{Error: fmt.Sprintf("%s: %v", tool, err), Code: -1}
}

// ---------------------------------------------------------------------------
// file_read
// ---------------------------------------------------------------------------

type FileReadTool struct {
	fileTool
	MaxOutput   int
	MaxLines    int
	ReadSecrets bool
}

func NewFileReadTool(root string) *FileReadTool {
	return &FileReadTool{
		fileTool:  newFileTool(root),
		MaxOutput: defaultFileReadMaxOutputSize,
		MaxLines:  defaultReadLimitLines,
	}
}

func (t *FileReadTool) Name() string { return "file_read" }

func (t *FileReadTool) Description() string {
	return "Read a file from disk and return its contents. For large files use " +
		"offset (1-indexed start line) and limit (max lines) to page through it."
}

func (t *FileReadTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"file_path": {
			"type": "string",
			"description": "path of the file to read, relative to the working directory"
		},
		"offset": {
			"type": "integer",
			"description": "optional 1-indexed line to start reading from"
		},
		"limit": {
			"type": "integer",
			"description": "optional maximum number of lines to read"
		}
	},
	"required": ["file_path"]
}`)
}

type fileReadArgs struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func (t *FileReadTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	var a fileReadArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return errOutput("file_read", fmt.Errorf("invalid arguments: %w", err))
	}
	path, err := t.resolveInRoot(a.FilePath)
	if err != nil {
		return errOutput("file_read", err)
	}
	if !t.ReadSecrets && isSecretFile(path) {
		return errOutput("file_read", fmt.Errorf(
			"reading %s is disabled (enable with --read-secrets)", a.FilePath))
	}
	info, err := os.Stat(path)
	if err != nil {
		return errOutput("file_read", fmt.Errorf("cannot access file: %w", err))
	}
	if info.IsDir() {
		return errOutput("file_read", fmt.Errorf("%s is a directory (use file_list)", a.FilePath))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errOutput("file_read", fmt.Errorf("failed to read file: %w", err))
	}

	lines := strings.Split(string(data), "\n")
	start := 0
	if a.Offset > 0 {
		start = a.Offset - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	limit := a.Limit
	if limit <= 0 || limit > t.maxLines() {
		limit = t.maxLines()
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	out := strings.Join(lines[start:end], "\n")
	if end < len(lines) {
		out += fmt.Sprintf("\n... [%d more lines] ...", len(lines)-end)
	}
	return types.ExecutionOutput{Output: truncateOutput(out, t.maxOutput()), Code: 0}
}

func (t *FileReadTool) maxOutput() int {
	if t.MaxOutput > 0 {
		return t.MaxOutput
	}
	return defaultFileReadMaxOutputSize
}

func (t *FileReadTool) maxLines() int {
	if t.MaxLines > 0 {
		return t.MaxLines
	}
	return defaultReadLimitLines
}

// ---------------------------------------------------------------------------
// file_write
// ---------------------------------------------------------------------------

type FileWriteTool struct {
	fileTool
}

func NewFileWriteTool(root string) *FileWriteTool {
	return &FileWriteTool{fileTool: newFileTool(root)}
}

func (t *FileWriteTool) Name() string { return "file_write" }

func (t *FileWriteTool) Description() string {
	return "Write content to a file, creating any missing parent directories. " +
		"Overwrites the file unless append is true."
}

func (t *FileWriteTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"file_path": {
			"type": "string",
			"description": "path of the file to write, relative to the working directory"
		},
		"content": {
			"type": "string",
			"description": "the content to write"
		},
		"append": {
			"type": "boolean",
			"description": "append to the file instead of overwriting it"
		}
	},
	"required": ["file_path", "content"]
}`)
}

type fileWriteArgs struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
	Append   bool   `json:"append"`
}

func (t *FileWriteTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	var a fileWriteArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return errOutput("file_write", fmt.Errorf("invalid arguments: %w", err))
	}
	path, err := t.resolveInRoot(a.FilePath)
	if err != nil {
		return errOutput("file_write", err)
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return errOutput("file_write", fmt.Errorf("%s is a directory", a.FilePath))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errOutput("file_write", fmt.Errorf("failed to create parent directories: %w", err))
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if a.Append {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return errOutput("file_write", fmt.Errorf("failed to open file: %w", err))
	}
	n, writeErr := f.WriteString(a.Content)
	closeErr := f.Close()
	if writeErr != nil {
		return errOutput("file_write", fmt.Errorf("failed to write file: %w", writeErr))
	}
	if closeErr != nil {
		return errOutput("file_write", fmt.Errorf("failed to write file: %w", closeErr))
	}
	verb := "wrote"
	if a.Append {
		verb = "appended"
	}
	return types.ExecutionOutput{
		Output: fmt.Sprintf("%s %d bytes to %s", verb, n, a.FilePath),
		Code:   0,
	}
}

// ---------------------------------------------------------------------------
// file_edit
// ---------------------------------------------------------------------------

type FileEditTool struct {
	fileTool
}

func NewFileEditTool(root string) *FileEditTool {
	return &FileEditTool{fileTool: newFileTool(root)}
}

func (t *FileEditTool) Name() string { return "file_edit" }

func (t *FileEditTool) Description() string {
	return "Replace an exact string in a file. old_string must appear exactly " +
		"once unless replace_all is true. Provide surrounding context to make it unique."
}

func (t *FileEditTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"file_path": {
			"type": "string",
			"description": "path of the file to edit, relative to the working directory"
		},
		"old_string": {
			"type": "string",
			"description": "the exact text to replace"
		},
		"new_string": {
			"type": "string",
			"description": "the replacement text"
		},
		"replace_all": {
			"type": "boolean",
			"description": "replace every occurrence instead of requiring a unique match"
		}
	},
	"required": ["file_path", "old_string", "new_string"]
}`)
}

type fileEditArgs struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *FileEditTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	var a fileEditArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return errOutput("file_edit", fmt.Errorf("invalid arguments: %w", err))
	}
	if a.OldString == "" {
		return errOutput("file_edit", errors.New("old_string cannot be empty"))
	}
	path, err := t.resolveInRoot(a.FilePath)
	if err != nil {
		return errOutput("file_edit", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return errOutput("file_edit", fmt.Errorf("cannot access file: %w", err))
	}
	if info.IsDir() {
		return errOutput("file_edit", fmt.Errorf("%s is a directory", a.FilePath))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errOutput("file_edit", fmt.Errorf("failed to read file: %w", err))
	}
	content := string(data)
	count := strings.Count(content, a.OldString)
	if count == 0 {
		return errOutput("file_edit", fmt.Errorf("old_string not found in %s", a.FilePath))
	}
	if count > 1 && !a.ReplaceAll {
		return errOutput("file_edit", fmt.Errorf(
			"old_string matches %d times in %s; add more context or set replace_all", count, a.FilePath))
	}
	replaced := 1
	var updated string
	if a.ReplaceAll {
		updated = strings.ReplaceAll(content, a.OldString, a.NewString)
		replaced = count
	} else {
		updated = strings.Replace(content, a.OldString, a.NewString, 1)
	}
	perm := info.Mode().Perm()
	if err := os.WriteFile(path, []byte(updated), perm); err != nil {
		return errOutput("file_edit", fmt.Errorf("failed to write file: %w", err))
	}
	return types.ExecutionOutput{
		Output: fmt.Sprintf("edited %s (%d replacement(s))", a.FilePath, replaced),
		Code:   0,
	}
}

// ---------------------------------------------------------------------------
// file_list
// ---------------------------------------------------------------------------

type FileListTool struct {
	fileTool
	MaxEntries int
}

func NewFileListTool(root string) *FileListTool {
	return &FileListTool{
		fileTool:   newFileTool(root),
		MaxEntries: defaultListMaxEntries,
	}
}

func (t *FileListTool) Name() string { return "file_list" }

func (t *FileListTool) Description() string {
	return "List the contents of a directory. Directories are shown with a " +
		"trailing slash and files with their size in bytes. Set recursive to " +
		"descend into subdirectories and pattern to filter entries."
}

func (t *FileListTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {
			"type": "string",
			"description": "directory to list, relative to the working directory (default \".\")"
		},
		"recursive": {
			"type": "boolean",
			"description": "recurse into subdirectories"
		},
		"pattern": {
			"type": "string",
			"description": "optional glob (e.g. \"*.go\") to filter entries by name"
		}
	},
	"required": []
}`)
}

type fileListArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	Pattern   string `json:"pattern"`
}

func (t *FileListTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	var a fileListArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return errOutput("file_list", fmt.Errorf("invalid arguments: %w", err))
	}
	dir, err := t.resolveInRoot(a.Path)
	if err != nil {
		return errOutput("file_list", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return errOutput("file_list", fmt.Errorf("cannot access path: %w", err))
	}
	if !info.IsDir() {
		return types.ExecutionOutput{Output: formatListEntry(filepath.Base(dir), info, false), Code: 0}
	}

	max := t.maxEntries()
	var b strings.Builder
	count := 0
	truncated := false
	emit := func(name string, d fs.DirEntry) bool {
		if a.Pattern != "" && !matchPattern(a.Pattern, name) {
			return true
		}
		if count >= max {
			truncated = true
			return false
		}
		fi, err := d.Info()
		if err != nil {
			return true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(formatListEntry(name, fi, d.IsDir()))
		count++
		return true
	}

	if a.Recursive {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || p == dir {
				return nil
			}
			if d.IsDir() && d.Name() == ignoredDirName {
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(dir, p)
			if relErr != nil {
				return nil
			}
			if !emit(filepath.ToSlash(rel), d) {
				return filepath.SkipAll
			}
			return nil
		})
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return errOutput("file_list", fmt.Errorf("failed to read directory: %w", err))
		}
		for _, d := range entries {
			if d.IsDir() && d.Name() == ignoredDirName {
				continue
			}
			if !emit(d.Name(), d) {
				break
			}
		}
	}

	if b.Len() == 0 {
		return types.ExecutionOutput{Output: "(empty)", Code: 0}
	}
	if truncated {
		fmt.Fprintf(&b, "\n... [truncated at %d entries]", max)
	}
	return types.ExecutionOutput{Output: b.String(), Code: 0}
}

func (t *FileListTool) maxEntries() int {
	if t.MaxEntries > 0 {
		return t.MaxEntries
	}
	return defaultListMaxEntries
}

func matchPattern(pattern, name string) bool {
	if ok, _ := filepath.Match(pattern, filepath.Base(name)); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, name); ok {
		return true
	}
	return false
}

func formatListEntry(name string, info os.FileInfo, isDir bool) string {
	if isDir {
		return name + "/"
	}
	return fmt.Sprintf("%s\t%d", name, info.Size())
}
