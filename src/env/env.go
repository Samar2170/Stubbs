package env

import (
	"context"
	"stubbs/src/types"
)

const DefaultTimeout = 300

type Environment interface {
	Execute(ctx context.Context, action types.ToolCall) types.ExecutionOutput
}

type EnvironmentConfig struct {
	WorkingDir string
	Env        map[string]string
	Timeout    int
}

type BaseEnvironment struct {
	config EnvironmentConfig
}
