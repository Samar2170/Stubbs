package env

import (
	"context"
	"fmt"
	"sort"
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
		return types.ExecutionOutput{
			Error: fmt.Sprintf("unknown tool %q; available: %v", action.Function.Name, le.ToolNames()),
		}
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

// Has reports whether the named tool is registered.
func (le *LocalEnvironment) Has(name string) bool {
	_, ok := le.tools.Get(name)
	return ok
}

// ToolNames returns every registered tool name in sorted order.
func (le *LocalEnvironment) ToolNames() []string {
	if le == nil || le.tools == nil {
		return nil
	}
	tools := le.tools.List()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	sort.Strings(names)
	return names
}

var _ Environment = (*LocalEnvironment)(nil)
