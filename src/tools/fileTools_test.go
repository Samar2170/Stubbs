package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testCtx = context.Background()

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileReadBasic(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "one\ntwo\nthree\n")
	out := NewFileReadTool(root).Execute(testCtx, `{"file_path":"a.txt"}`)
	if out.Code != 0 {
		t.Fatalf("code=%d error=%q", out.Code, out.Error)
	}
	if out.Output != "one\ntwo\nthree\n" {
		t.Fatalf("output=%q", out.Output)
	}
}

func TestFileReadOffsetLimit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "one\ntwo\nthree\n")
	out := NewFileReadTool(root).Execute(testCtx, `{"file_path":"a.txt","offset":2,"limit":1}`)
	if out.Code != 0 || !strings.HasPrefix(out.Output, "two") {
		t.Fatalf("code=%d output=%q error=%q", out.Code, out.Output, out.Error)
	}
	if !strings.Contains(out.Output, "more lines") {
		t.Fatalf("expected truncation marker, got %q", out.Output)
	}
}

func TestFileReadDirectoryRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := NewFileReadTool(root).Execute(testCtx, `{"file_path":"sub"}`)
	if out.Code != -1 || !strings.Contains(out.Error, "directory") {
		t.Fatalf("expected directory error, got code=%d error=%q", out.Code, out.Error)
	}
}

func TestFileReadEscapeRejected(t *testing.T) {
	root := t.TempDir()
	out := NewFileReadTool(root).Execute(testCtx, `{"file_path":"../secret.txt"}`)
	if out.Code != -1 || !strings.Contains(out.Error, "outside") {
		t.Fatalf("expected escape rejection, got code=%d error=%q", out.Code, out.Error)
	}

	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret")
	absArgs := fmt.Sprintf(`{"file_path":%q}`, filepath.Join(outside, "secret.txt"))
	out = NewFileReadTool(root).Execute(testCtx, absArgs)
	if out.Code != -1 || !strings.Contains(out.Error, "outside") {
		t.Fatalf("expected absolute escape rejection, got code=%d error=%q", out.Code, out.Error)
	}
}

func TestFileReadSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "target.txt"), "secret")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out := NewFileReadTool(root).Execute(testCtx, `{"file_path":"link/target.txt"}`)
	if out.Code != -1 || !strings.Contains(out.Error, "outside") {
		t.Fatalf("expected symlink escape rejection, got code=%d error=%q", out.Code, out.Error)
	}
}

func TestFileReadSecretBlockedByDefault(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".env"), "KEY=secret\n")
	mustWrite(t, filepath.Join(root, ".env.local"), "KEY=secret\n")
	tool := NewFileReadTool(root)
	for _, name := range []string{".env", ".env.local"} {
		out := tool.Execute(testCtx, fmt.Sprintf(`{"file_path":%q}`, name))
		if out.Code != -1 || !strings.Contains(out.Error, "disabled") {
			t.Fatalf("%s: expected block, got code=%d error=%q", name, out.Code, out.Error)
		}
	}
	tool.ReadSecrets = true
	out := tool.Execute(testCtx, `{"file_path":".env"}`)
	if out.Code != 0 || !strings.Contains(out.Output, "secret") {
		t.Fatalf("expected read with ReadSecrets, got code=%d output=%q error=%q", out.Code, out.Output, out.Error)
	}
}

func TestFileListIgnoresStubbs(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "a\n")
	mustWrite(t, filepath.Join(root, ignoredDirName, "session.json"), "{}\n")
	mustWrite(t, filepath.Join(root, "sub", ignoredDirName, "log"), "x\n")
	tool := NewFileListTool(root)
	if out := tool.Execute(testCtx, `{}`); strings.Contains(out.Output, ignoredDirName) {
		t.Fatalf("non-recursive list leaked %s: %q", ignoredDirName, out.Output)
	}
	out := tool.Execute(testCtx, `{"recursive":true}`)
	if strings.Contains(out.Output, ignoredDirName) {
		t.Fatalf("recursive list leaked %s: %q", ignoredDirName, out.Output)
	}
	if !strings.Contains(out.Output, "a.txt") || !strings.Contains(out.Output, "sub/") {
		t.Fatalf("recursive list missing entries: %q", out.Output)
	}
}

func TestFileWriteCreatesParentsAndReadsBack(t *testing.T) {
	root := t.TempDir()
	out := NewFileWriteTool(root).Execute(testCtx, `{"file_path":"sub/deep/b.txt","content":"hello"}`)
	if out.Code != 0 {
		t.Fatalf("code=%d error=%q", out.Code, out.Error)
	}
	data, err := os.ReadFile(filepath.Join(root, "sub", "deep", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("content=%q", data)
	}
}

func TestFileWriteAppend(t *testing.T) {
	root := t.TempDir()
	tool := NewFileWriteTool(root)
	tool.Execute(testCtx, `{"file_path":"a.txt","content":"one"}`)
	out := tool.Execute(testCtx, `{"file_path":"a.txt","content":"two","append":true}`)
	if out.Code != 0 {
		t.Fatalf("code=%d error=%q", out.Code, out.Error)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "onetwo" {
		t.Fatalf("content=%q", data)
	}
}

func TestFileWriteEscapeRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)
	out := NewFileWriteTool(root).Execute(testCtx, `{"file_path":"../evil.txt","content":"x"}`)
	if out.Code != -1 {
		t.Fatalf("expected rejection, got code=%d error=%q", out.Code, out.Error)
	}
	if _, err := os.Stat(filepath.Join(parent, "evil.txt")); err == nil {
		t.Fatal("file was written outside the root")
	}
}

func TestFileList(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package a\n")
	mustWrite(t, filepath.Join(root, "b.txt"), "hello\n")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := NewFileListTool(root).Execute(testCtx, `{}`)
	if out.Code != 0 {
		t.Fatalf("code=%d error=%q", out.Code, out.Error)
	}
	for _, want := range []string{"a.go", "b.txt", "sub/"} {
		if !strings.Contains(out.Output, want) {
			t.Fatalf("output %q missing %q", out.Output, want)
		}
	}
}

func TestFileListRecursivePattern(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package a\n")
	mustWrite(t, filepath.Join(root, "sub", "c.go"), "package c\n")
	mustWrite(t, filepath.Join(root, "sub", "d.txt"), "d\n")
	out := NewFileListTool(root).Execute(testCtx, `{"recursive":true,"pattern":"*.go"}`)
	if out.Code != 0 {
		t.Fatalf("code=%d error=%q", out.Code, out.Error)
	}
	if !strings.Contains(out.Output, "a.go") || !strings.Contains(out.Output, "sub/c.go") {
		t.Fatalf("output=%q", out.Output)
	}
	if strings.Contains(out.Output, "d.txt") {
		t.Fatalf("pattern not applied: %q", out.Output)
	}
}

func TestFileEditReplace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.go")
	mustWrite(t, path, "package main\n\nfunc main() {}\n")
	out := NewFileEditTool(root).Execute(testCtx,
		`{"file_path":"x.go","old_string":"func main() {}","new_string":"func main() { println(1) }"}`)
	if out.Code != 0 {
		t.Fatalf("code=%d error=%q", out.Code, out.Error)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "println(1)") {
		t.Fatalf("content=%q", data)
	}
}

func TestFileEditErrors(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "x.txt"), "aaa\naaa\n")
	tool := NewFileEditTool(root)

	if out := tool.Execute(testCtx, `{"file_path":"x.txt","old_string":"","new_string":"b"}`); out.Code != -1 {
		t.Fatalf("empty old_string should fail, got %+v", out)
	}
	if out := tool.Execute(testCtx, `{"file_path":"x.txt","old_string":"zzz","new_string":"b"}`); out.Code != -1 {
		t.Fatalf("missing old_string should fail, got %+v", out)
	}
	if out := tool.Execute(testCtx, `{"file_path":"x.txt","old_string":"aaa","new_string":"b"}`); out.Code != -1 {
		t.Fatalf("non-unique match should fail, got %+v", out)
	}
}

func TestFileEditReplaceAll(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	mustWrite(t, path, "aaa\naaa\n")
	out := NewFileEditTool(root).Execute(testCtx,
		`{"file_path":"x.txt","old_string":"aaa","new_string":"b","replace_all":true}`)
	if out.Code != 0 {
		t.Fatalf("code=%d error=%q", out.Code, out.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "b\nb\n" {
		t.Fatalf("content=%q", data)
	}
}
