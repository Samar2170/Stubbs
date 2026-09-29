package agent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"stubbs/src/prompts"
	"stubbs/src/types"
)

const (
	repoDigestMaxFiles = 60
	repoDigestFileMax  = 12 << 10
	repoDigestMaxBytes = 96 << 10
)

var repoSkipDirs = map[string]bool{
	".git": true, ".stubbs": true, ".idea": true, ".vscode": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "out": true, "__pycache__": true, ".venv": true,
	"venv": true, "bench": true, ".cache": true,
}

var repoTextExt = map[string]bool{
	".go": true, ".md": true, ".txt": true, ".json": true, ".yaml": true,
	".yml": true, ".toml": true, ".mod": true, ".sum": true, ".js": true,
	".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".py": true, ".rs": true, ".java": true, ".kt": true, ".rb": true,
	".php": true, ".cs": true, ".c": true, ".cc": true, ".cpp": true,
	".h": true, ".hpp": true, ".sh": true, ".bash": true, ".mk": true,
	".cfg": true, ".ini": true, ".sql": true, ".proto": true, ".html": true,
	".css": true, ".scss": true, ".xml": true,
}

var repoPriorityFiles = map[string]int{
	"readme.md": 0, "readme": 0, "go.mod": 1, "package.json": 1,
	"cargo.toml": 1, "pyproject.toml": 1, "makefile": 2, "dockerfile": 2,
}

func (a *Agent) GenerateRepoMap(ctx context.Context) (string, error) {
	if a == nil || a.Memory == nil {
		return "", fmt.Errorf("agent: memory is not configured")
	}
	root := strings.TrimSpace(a.WorkingDir)
	if root == "" {
		root = "."
	}
	digest, err := buildRepoDigest(root)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(digest) == "" {
		return "", fmt.Errorf("agent: no repository files found under %s", root)
	}

	resp, err := a.completeText(ctx, []types.Message{
		{Role: types.RoleSystem, Content: prompts.RepoMap},
		{Role: types.RoleUser, Content: "Repository digest:\n\n" + digest},
	})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(resp)
	if out == "" {
		return "", fmt.Errorf("agent: repository map response was empty")
	}
	if err := a.Memory.WriteRepoMap(out); err != nil {
		return "", err
	}
	return out, nil
}

func (a *Agent) ensureRepoMap(ctx context.Context) {
	if a == nil || a.Memory == nil || !a.Memory.Options().AutoRepoMap {
		return
	}
	if existing, err := a.Memory.RepoMap(); err == nil && existing != "" {
		return
	}
	_, _ = a.GenerateRepoMap(ctx)
}

func buildRepoDigest(root string) (string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && repoSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("agent: walk %s: %w", root, err)
	}
	sort.Slice(paths, func(i, j int) bool {
		pi, pj := repoFilePriority(paths[i]), repoFilePriority(paths[j])
		if pi != pj {
			return pi < pj
		}
		return paths[i] < paths[j]
	})

	var b strings.Builder
	b.WriteString("Repository file tree:\n")
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte('\n')
	}

	b.WriteString("\nKey file contents:\n")
	total := 0
	included := 0
	for _, p := range paths {
		if included >= repoDigestMaxFiles || total >= repoDigestMaxBytes {
			break
		}
		if !repoIsText(p) {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		info, statErr := os.Stat(full)
		if statErr != nil || info.Size() > repoDigestFileMax {
			continue
		}
		data, readErr := os.ReadFile(full)
		if readErr != nil {
			continue
		}
		remaining := repoDigestMaxBytes - total
		if len(data) > remaining {
			data = data[:remaining]
		}
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", p, strings.TrimRight(string(data), "\n"))
		total += len(data)
		included++
	}
	return b.String(), nil
}

func repoFilePriority(rel string) int {
	base := strings.ToLower(filepath.Base(rel))
	if p, ok := repoPriorityFiles[base]; ok {
		return p
	}
	return 10
}

func repoIsText(rel string) bool {
	base := filepath.Base(rel)
	if strings.HasPrefix(base, ".env") {
		return false
	}
	return repoTextExt[strings.ToLower(filepath.Ext(base))]
}
