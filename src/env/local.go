package env

import (
	"context"
	"stubbs/src/types"
	"time"
)

type LocalEnvironment struct {
	config EnvironmentConfig
	tools  *types.Registry
}

func NewLocalEnvironment(config EnvironmentConfig, tools *types.Registry) *LocalEnvironment {
	return &LocalEnvironment{
		config: config,
		tools:  tools,
	}
}

func (le *LocalEnvironment) Execute(ctx context.Context, action types.ToolCall) types.ExecutionOutput {
	tool, ok := le.tools.Get(action.Function.Name)
	if !ok {
		return types.ExecutionOutput{Error: "tool not found", Code: -1}
	}
	timeout := le.config.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	st := time.Now()
	out := tool.Execute(runCtx, action.Function.Arguments)
	out.Duration = time.Since(st)
	return out
}

var _ Environment = (*LocalEnvironment)(nil)
