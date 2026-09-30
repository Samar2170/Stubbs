package env

import (
	"context"
	"stubbs/src/types"
)

const DefaultTimeout = 300

type Environment interface {
	Execute(ctx context.Context, action types.ToolCall) types.ExecutionOutput
	// Has reports whether a tool with the given name is registered. It lets the
	// agent reject hallucinated tool calls before asking the user to approve
	// them.
	Has(name string) bool
	// ToolNames returns the names of every registered tool, sorted.
	ToolNames() []string
}

type EnvironmentConfig struct {
	WorkingDir string
	Env        map[string]string
	Timeout    int
}

type BaseEnvironment struct {
	config EnvironmentConfig
}
