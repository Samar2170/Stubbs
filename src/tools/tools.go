package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"stubbs/src/types"
	"time"
)

const (
	defaultBashTimeout   = 300 * time.Second
	defaultMaxOutputSize = 16 << 10
)

type BashTool struct {
	Timeout   time.Duration
	MaxOutput int
	Dir       string
	// Confine sandboxes each command so writes are limited to Dir. See
	// bashSandbox for the guarantees and requirements.
	Confine bool
	// SandboxBackend selects the backend ("auto" or "bwrap"); empty means auto.
	SandboxBackend string
	// SandboxHome is a host directory used as HOME inside the sandbox so caches
	// persist. Empty uses a private tmpfs home.
	SandboxHome string
	// AllowUnsandboxed runs commands unconfined when Confine is set but no
	// sandbox backend is available, instead of failing.
	AllowUnsandboxed bool
	// LookPath is injectable for tests; defaults to exec.LookPath.
	LookPath func(string) (string, error)
}

func NewBashTool() *BashTool {
	return &BashTool{
		Timeout:   defaultBashTimeout,
		MaxOutput: defaultMaxOutputSize,
		Dir:       ".",
	}
}

func (b *BashTool) Name() string { return "bash" }

func (b *BashTool) Description() string {
	return "Run a bash command on the user's machine and return its " +
		"combined stdout/stderr and exit code."
}

func (b *BashTool) Parameters() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The bash command to execute",
			},
		},
		"required": []string{"command"},
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return []byte(`{"type":"object"}`)
	}
	return encoded
}

type bashArgs struct {
	Command string `json:"command"`
}

func (b *BashTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	var input bashArgs
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("bash: bad arguments: %v", err), Code: -1}
	}
	if strings.TrimSpace(input.Command) == "" {
		return types.ExecutionOutput{Error: "bash: command cannot be empty", Code: -1}
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = defaultBashTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv := []string{"bash", "-c", input.Command}
	runDir := b.Dir
	if b.Confine {
		spec, err := (bashSandbox{
			Root:             b.Dir,
			WorkDir:          b.Dir,
			Backend:          b.SandboxBackend,
			Home:             b.SandboxHome,
			AllowUnsandboxed: b.AllowUnsandboxed,
			LookPath:         b.LookPath,
		}).Command(input.Command)
		switch {
		case err == nil:
			argv = spec.Argv
			runDir = spec.Dir
		case b.AllowUnsandboxed && errors.Is(err, errNoSandbox):
			// Fall back to running unconfined.
		default:
			return types.ExecutionOutput{Error: fmt.Sprintf("bash: %v", err), Code: -1}
		}
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = runDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	out := types.ExecutionOutput{
		Output: truncateOutput(buf.String(), defaultMaxOutputSize),
		Code:   exitCode(runErr, runCtx),
	}
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); !ok {
			out.Error = runErr.Error()
		}
	}
	return out
}

// exitCode maps exec errors to an exit status; context deadline surfaces
// as code -1 with a timeout marker so the model can react to hangs.
func exitCode(err error, ctx context.Context) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if ctx.Err() != nil {
		return -1
	}
	return -1
}

// truncateOutput keeps the head and tail of oversized output.
func truncateOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	keep := max / 2
	marker := fmt.Sprintf("\n... [output truncated: %d bytes dropped] ...\n", len(s)-2*keep)
	return s[:keep] + marker + s[len(s)-keep:]
}
