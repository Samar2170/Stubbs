package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireSandbox skips tests that need a real bubblewrap backend.
func requireSandbox(t *testing.T) {
	t.Helper()
	if !SandboxAvailable("bwrap") {
		t.Skip("bwrap not available")
	}
}

func TestBashSandboxBlocksOutsideWrites(t *testing.T) {
	requireSandbox(t)

	root := t.TempDir()
	outside := t.TempDir()

	b := NewBashTool()
	b.Dir = root
	b.Confine = true
	b.SandboxHome = t.TempDir()

	inside := filepath.Join(root, "inside.txt")
	out := b.Execute(context.Background(), `{"command":"echo hi > inside.txt"}`)
	if out.Code != 0 || out.Error != "" {
		t.Fatalf("inside write failed: %+v", out)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("inside file missing: %v", err)
	}

	escape := filepath.Join(outside, "escape.txt")
	out = b.Execute(context.Background(), `{"command":"echo hi > `+escape+`"}`)
	if out.Code == 0 {
		t.Fatalf("outside write unexpectedly succeeded: %+v", out)
	}
	if _, err := os.Stat(escape); err == nil {
		t.Fatal("outside file was written despite confinement")
	}
}

func TestBashConfineKeepsWorkingDir(t *testing.T) {
	requireSandbox(t)

	root := t.TempDir()
	want, err := canonicalDir(root)
	if err != nil {
		t.Fatal(err)
	}

	b := NewBashTool()
	b.Dir = root
	b.Confine = true
	b.SandboxHome = t.TempDir()

	out := b.Execute(context.Background(), `{"command":"pwd"}`)
	if out.Code != 0 {
		t.Fatalf("pwd failed: %+v", out)
	}
	if got := strings.TrimSpace(out.Output); got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
}

func TestBashSandboxNoBackendFailsClosed(t *testing.T) {
	root := t.TempDir()
	b := NewBashTool()
	b.Dir = root
	b.Confine = true
	b.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	out := b.Execute(context.Background(), `{"command":"echo hi"}`)
	if out.Code == 0 || out.Error == "" {
		t.Fatalf("expected fail-closed error, got %+v", out)
	}
}

func TestBashConfineAllowUnsandboxed(t *testing.T) {
	root := t.TempDir()
	b := NewBashTool()
	b.Dir = root
	b.Confine = true
	b.AllowUnsandboxed = true
	b.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	out := b.Execute(context.Background(), `{"command":"echo hi"}`)
	if out.Code != 0 {
		t.Fatalf("expected unconfined fallback to run, got %+v", out)
	}
}

func TestBashSandboxRejectsWorkDirOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	s := bashSandbox{
		Root:     root,
		WorkDir:  outside,
		LookPath: func(string) (string, error) { return "/usr/bin/bwrap", nil },
	}
	if _, err := s.Command("true"); err == nil {
		t.Fatal("expected error for workdir outside root")
	}
}

func TestBashSandboxUnknownBackend(t *testing.T) {
	root := t.TempDir()
	s := bashSandbox{Root: root, WorkDir: root, Backend: "nope"}
	if _, err := s.Command("true"); err == nil {
		t.Fatal("expected unknown backend error")
	}
}

func TestBashSandboxNoBackendError(t *testing.T) {
	root := t.TempDir()
	s := bashSandbox{
		Root:     root,
		WorkDir:  root,
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}
	if _, err := s.Command("true"); !errors.Is(err, errNoSandbox) {
		t.Fatalf("err = %v, want errNoSandbox", err)
	}
}
