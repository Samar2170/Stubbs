package env

// import (
// 	"bytes"
// 	"context"
// 	"encoding/json"
// 	"fmt"
// 	"os/exec"
// 	"strings"
// 	"stubbs/src/types"
// 	"syscall"
// 	"time"
// )

// type LocalEnvironment struct {
// 	BaseEnvironment
// }

// func NewLocalEnvironment(workingDir, env string, timeout int) *LocalEnvironment {
// 	return &LocalEnvironment{
// 		BaseEnvironment: BaseEnvironment{
// 			config: EnvironmentConfig{
// 				WorkingDir: workingDir,
// 				Timeout:    timeout,
// 			},
// 		},
// 	}
// }

// func (le *LocalEnvironment) Execute(action types.ToolCall, wd string, timeout int) types.ExecutionOutput {
// 	command, err := parseCommand(action)
// 	if err != nil {
// 		return types.ExecutionOutput{
// 			Error:  err.Error(),
// 			Code:   -1,
// 			Output: "",
// 		}
// 	}
// 	var cwd string
// 	if wd != "" {
// 		cwd = wd
// 	} else {
// 		cwd = le.config.WorkingDir
// 	}
// 	fmt.Println("command:", command)
// 	st := time.Now()
// 	result, exitCode, runErr := run(command, cwd, timeout)
// 	var errMsg string
// 	if runErr != nil {
// 		errMsg = runErr.Error()
// 	}
// 	output := types.ExecutionOutput{
// 		Output:   result,
// 		Code:     exitCode,
// 		Error:    errMsg,
// 		Duration: time.Now().Sub(st),
// 	}
// 	return output
// }

// // parseCommand extracts the shell command from the tool call arguments,
// // which are a JSON object such as {"command":"ls -la"}.
// func parseCommand(action types.ToolCall) (string, error) {
// 	var args struct {
// 		Command string `json:"command"`
// 	}
// 	if err := json.Unmarshal([]byte(action.Function.Arguments), &args); err != nil {
// 		return "", fmt.Errorf("local: bad tool arguments: %w", err)
// 	}
// 	if strings.TrimSpace(args.Command) == "" {
// 		return "", fmt.Errorf("local: no command in tool arguments")
// 	}
// 	return args.Command, nil
// }

// func run(command string, cwd string, timeout int) (string, int, error) {
// 	// timeout is in seconds; time.Duration(timeout) alone would be nanoseconds.
// 	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
// 	defer cancel()

// 	cmd := exec.CommandContext(ctx, "sh", "-c", command)
// 	cmd.Dir = cwd
// 	// cmd.Env = env
// 	var buf bytes.Buffer
// 	cmd.Stdout = &buf
// 	cmd.Stderr = &buf

// 	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
// 	cmd.Cancel = func() error {
// 		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
// 	}
// 	cmd.WaitDelay = 5 * time.Second
// 	err := cmd.Run()
// 	exitCode := -1
// 	if cmd.ProcessState != nil {
// 		exitCode = cmd.ProcessState.ExitCode()
// 	}
// 	return buf.String(), exitCode, err
// }
