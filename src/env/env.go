package env

import (
	"stubbs/src/types"
	"time"
)

type Environment interface {
	Execute(action types.ToolCall, cwd string, timeout int) ExecutionOutput
}
type ExecutionOutput struct {
	Output   string
	Error    string
	Code     int
	Duration time.Duration
}

type EnvironmentConfig struct {
	WorkingDir string
	Env        map[string]string
	Timeout    int
}

type BaseEnvironment struct {
	config EnvironmentConfig
}
