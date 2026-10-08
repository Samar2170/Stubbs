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

// requireSandbox skips tests that need a working bubblewrap backend.
func requireSandbox(t *testing.T) {
	t.Helper()
	if !SandboxAvailable("bwrap") {
		t.Skip("bwrap not available or cannot create namespaces")
	}
}

func TestBwrapArgvConfinesReadsWritesAndNetwork(t *testing.T) {
	argv := bwrapArgv("/usr/bin/bwrap", "/work", "/work/sub", "/home/me", "echo hi")
	joined := strings.Join(argv, " ")

	for _, want := range []string{
		"--ro-bind / /",
		"--bind /work /work",
		"--unshare-net",
		"--clearenv",
		"--setenv PATH " + sandboxPath,
		"--setenv HOME /home/me",
		"--chdir /work/sub",
		"-- bash -c echo hi",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q:\n%s", want, joined)
		}
	}
}

func TestBwrapArgvPrivateHome(t *testing.T) {
	argv := bwrapArgv("bwrap", "/work", "/work", "", "true")
	joined := strings.Join(argv, " ")

	if !strings.Contains(joined, "--dir "+sandboxHome) {
		t.Errorf("expected private home dir %s in:\n%s", sandboxHome, joined)
	}
	if !strings.Contains(joined, "--setenv HOME "+sandboxHome) {
		t.Errorf("expected HOME=%s in:\n%s", sandboxHome, joined)
	}
}

func TestBashSandboxCommandNoBackend(t *testing.T) {
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

func TestSandboxAvailableUnknownBackend(t *testing.T) {
	if SandboxAvailable("nope") {
		t.Fatal("unknown backend should be unavailable")
	}
}

func TestCanonicalDir(t *testing.T) {
	root := t.TempDir()
	got, err := canonicalDir(root)
	if err != nil {
		t.Fatalf("canonicalDir(%q) error: %v", root, err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("canonicalDir returned non-absolute %q", got)
	}
	if _, err := canonicalDir(filepath.Join(root, "missing")); err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestIsWithin(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"root", root, true},
		{"sub", sub, true},
		{"sibling", root + "-x", false},
		{"parent", filepath.Dir(root), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWithin(root, tc.path); got != tc.want {
				t.Fatalf("isWithin(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
			}
		})
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

func TestBashConfineBlocksNetwork(t *testing.T) {
	requireSandbox(t)

	root := t.TempDir()
	b := NewBashTool()
	b.Dir = root
	b.Confine = true
	b.SandboxHome = t.TempDir()

	out := b.Execute(context.Background(), `{"command":"{ exec 3<>/dev/tcp/1.1.1.1/80; } 2>/dev/null && echo reachable || echo blocked"}`)
	if out.Code != 0 {
		t.Fatalf("network check failed: %+v", out)
	}
	if got := strings.TrimSpace(out.Output); got != "blocked" {
		t.Fatalf("expected network to be blocked, got %q", got)
	}
}

func TestBashConfineClearsEnvironment(t *testing.T) {
	requireSandbox(t)

	t.Setenv("STUBBS_SANDBOX_TEST_SECRET", "leak")

	root := t.TempDir()
	b := NewBashTool()
	b.Dir = root
	b.Confine = true
	b.SandboxHome = t.TempDir()

	out := b.Execute(context.Background(), `{"command":"echo ${STUBBS_SANDBOX_TEST_SECRET:-unset}"}`)
	if out.Code != 0 {
		t.Fatalf("env check failed: %+v", out)
	}
	if got := strings.TrimSpace(out.Output); got != "unset" {
		t.Fatalf("secret leaked into sandbox: output = %q", got)
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
