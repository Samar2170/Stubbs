package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
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

type bashResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func (b *BashTool) Execute(ctx context.Context, args string) (json.RawMessage, error) {
	var input bashArgs
	// if err := json.Unmarshal(args, &input); err != nil {
	// 	return nil, fmt.Errorf("bash: bad arguments: %w", err)
	// }
	if strings.TrimSpace(input.Command) == "" {
		return nil, fmt.Errorf("bash: command cannot be empty")
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = defaultBashTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "bash", "-c", input.Command)
	cmd.Dir = b.Dir
	out, err := cmd.CombinedOutput()
	res := bashResult{
		Output:   truncateOutput(string(out), b.maxOutput()),
		ExitCode: exitCode(err, runCtx),
	}
	encoded, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("bash: failed to encode result: %w", err)
	}
	return encoded, nil
}

func (b *BashTool) maxOutput() int {
	if b.MaxOutput > 0 {
		return b.MaxOutput
	}
	return defaultMaxOutputSize
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
