package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListFilesSkipsNoiseDirectories(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.go"), "package main")
	writeTestFile(t, filepath.Join(root, "docs", "readme.md"), "# hi")
	writeTestFile(t, filepath.Join(root, ".git", "config"), "x")
	writeTestFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "x")

	got := ListFiles(root)
	want := []string{"docs/readme.md", "main.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListFiles = %v, want %v", got, want)
	}
}

func TestExpandFileMentionsAttachesContents(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\n")

	got := ExpandFileMentions("explain @main.go please", root)
	if !strings.Contains(got, "explain @main.go please") {
		t.Errorf("original prompt should be preserved:\n%s", got)
	}
	if !strings.Contains(got, "Files the user attached with @:") {
		t.Errorf("missing attachment header:\n%s", got)
	}
	if !strings.Contains(got, "--- main.go ---") {
		t.Errorf("missing file label:\n%s", got)
	}
	if !strings.Contains(got, "func main() {}") {
		t.Errorf("missing file contents:\n%s", got)
	}
}

func TestExpandFileMentionsIgnoresEmailAndMissingFiles(t *testing.T) {
	root := t.TempDir()
	text := "mail foo@bar.com about @missing.go"
	if got := ExpandFileMentions(text, root); got != text {
		t.Fatalf("ExpandFileMentions = %q, want unchanged %q", got, text)
	}
}

func TestExpandFileMentionsRejectsOutsideWorkdir(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(parent, "secret.txt"), "secret")
	text := "read @../secret.txt"
	if got := ExpandFileMentions(text, root); got != text {
		t.Fatalf("out-of-tree mention should not expand:\n%s", got)
	}
}

func TestExpandFileMentionsSkipsBinaryAndDirectories(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "bin.dat"), "a\x00b")
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "look at @bin.dat and @dir"
	if got := ExpandFileMentions(text, root); got != text {
		t.Fatalf("binary/directory mentions should not expand:\n%s", got)
	}
}

func TestExpandFileMentionsDeduplicates(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "once")
	got := ExpandFileMentions("@a.txt and again @a.txt", root)
	if n := strings.Count(got, "--- a.txt ---"); n != 1 {
		t.Fatalf("file attached %d times, want 1:\n%s", n, got)
	}
}
