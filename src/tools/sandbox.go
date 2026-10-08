package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var errNoSandbox = errors.New("no sandbox backend available")

// sandboxHome is the in-sandbox HOME used when no persistent host home is
// configured; it lives on the private /tmp tmpfs.
const sandboxHome = "/tmp/stubbs-home"

// sandboxPath is the PATH set inside the sandbox after the environment is
// cleared, so commands can still resolve binaries.
const sandboxPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// sandboxProbeTimeout bounds the startup check that a backend actually works.
const sandboxProbeTimeout = 5 * time.Second

// commandSpec is the resolved way to run a command: a full argv (no shell
// interpretation), the working directory, and whether the result is actually
// sandboxed.
type commandSpec struct {
	Argv      []string
	Dir       string
	Sandboxed bool
}

// bashSandbox wraps shell commands in a bubblewrap sandbox. Within it the host
// filesystem is mounted read-only, so writes are confined to Root; the network
// is unshared; and the environment is cleared (only PATH and HOME are set), so
// host secrets are not readable via the environment.
type bashSandbox struct {
	// Root is the directory writes are confined to (the working directory).
	Root string
	// WorkDir is the directory the command starts in.
	WorkDir string
	// Backend names the sandbox implementation to use ("auto" or "bwrap").
	Backend string
	// Home is a host directory bind-mounted writable and used as HOME, so
	// caches persist across commands. Empty uses a private tmpfs home.
	Home string
	// AllowUnsandboxed runs unconfined when no backend is available instead of
	// failing. It is only consulted when confinement is requested.
	AllowUnsandboxed bool
	// LookPath is injectable for tests; defaults to exec.LookPath.
	LookPath func(string) (string, error)
}

func (s bashSandbox) lookPath(name string) (string, error) {
	if s.LookPath != nil {
		return s.LookPath(name)
	}
	return exec.LookPath(name)
}

// Command resolves a shell command line into the argv/directory that should be
// executed. It returns an error wrapping errNoSandbox when confinement was
// requested but no backend is installed.
func (s bashSandbox) Command(cmd string) (commandSpec, error) {
	root, err := canonicalDir(s.Root)
	if err != nil {
		return commandSpec{}, err
	}
	work, err := canonicalDir(s.WorkDir)
	if err != nil {
		return commandSpec{}, err
	}
	if !isWithin(root, work) {
		return commandSpec{}, fmt.Errorf("working directory %s is outside the sandbox root %s", work, root)
	}

	backend := strings.TrimSpace(s.Backend)
	if backend == "" {
		backend = "auto"
	}
	switch backend {
	case "auto", "bwrap":
		bwrap, lookErr := s.lookPath("bwrap")
		if lookErr != nil {
			return commandSpec{}, fmt.Errorf("%w: bubblewrap (bwrap) not found; install it or disable sandboxing", errNoSandbox)
		}
		home := strings.TrimSpace(s.Home)
		if home != "" {
			if abs, err := filepath.Abs(home); err == nil {
				home = abs
			}
			if err := os.MkdirAll(home, 0o755); err != nil {
				return commandSpec{}, fmt.Errorf("create sandbox home %s: %w", home, err)
			}
		}
		return commandSpec{Argv: bwrapArgv(bwrap, root, work, home, cmd), Dir: work, Sandboxed: true}, nil
	default:
		return commandSpec{}, fmt.Errorf("unknown bash sandbox backend %q", backend)
	}
}

// SandboxAvailable reports whether a usable backend is installed on this host.
// It probes the backend rather than trusting PATH, because bubblewrap can be
// present but unable to create namespaces (AppArmor/userns restrictions).
func SandboxAvailable(backend string) bool {
	switch strings.TrimSpace(backend) {
	case "", "auto", "bwrap":
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			return false
		}
		return probeSandbox(bwrap)
	default:
		return false
	}
}

// probeSandbox runs a representative sandboxed command to confirm the backend
// can actually start, so callers do not enable confinement that will fail on
// every command.
func probeSandbox(bwrap string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bwrap,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--unshare-net",
		"--clearenv",
		"--setenv", "PATH", sandboxPath,
		"--", "true",
	)
	return cmd.Run() == nil
}

func bwrapArgv(bwrap, root, work, home, cmd string) []string {
	argv := []string{
		bwrap,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--clearenv",
		"--setenv", "PATH", sandboxPath,
		"--unshare-net",
		"--bind", root, root,
	}
	if home != "" {
		argv = append(argv, "--bind", home, home, "--setenv", "HOME", home)
	} else {
		argv = append(argv, "--dir", sandboxHome, "--setenv", "HOME", sandboxHome)
	}
	argv = append(argv,
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
		"--new-session",
		"--chdir", work,
		"--", "bash", "-c", cmd,
	)
	return argv
}

func canonicalDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("sandbox root %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sandbox root %s is not a directory", abs)
	}
	return abs, nil
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
