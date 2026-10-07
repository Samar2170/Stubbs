package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchToolFindsMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("func main in a text file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewSearchTool(dir)
	out := tool.Execute(context.Background(), `{"pattern":"func main","include":"*.go"}`)
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if !strings.Contains(out.Output, "a.go:3:func main()") {
		t.Fatalf("expected match in a.go, got %q", out.Output)
	}
	if strings.Contains(out.Output, "b.txt") {
		t.Fatalf("include filter ignored: %q", out.Output)
	}
}

func TestSearchToolNoMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewSearchTool(dir)
	out := tool.Execute(context.Background(), `{"pattern":"does-not-exist"}`)
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if !strings.Contains(out.Output, "no matches") {
		t.Fatalf("expected no-match message, got %q", out.Output)
	}
}

func TestSearchToolBadPattern(t *testing.T) {
	tool := NewSearchTool(t.TempDir())
	out := tool.Execute(context.Background(), `{"pattern":"("}`)
	if out.Error == "" {
		t.Fatalf("invalid regexp should error, got %q", out.Output)
	}
}

func TestSearchToolRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	tool := NewSearchTool(dir)
	out := tool.Execute(context.Background(), `{"pattern":"root","path":"../../etc"}`)
	if out.Error == "" {
		t.Fatal("path escaping the working directory must be rejected")
	}
}
