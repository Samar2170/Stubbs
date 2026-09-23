package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"stubbs/src/agent"
	"stubbs/src/config"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/tools"
	"stubbs/src/tui"
	"stubbs/src/types"
	"sync"
	"syscall"

	"github.com/spf13/pflag"
)

const (
	defaultStepLimit = 24
	defaultCostLimit = 5.0
)

func run() error {
	fs := pflag.NewFlagSet("stubbs", pflag.ExitOnError)
	fs.SortFlags = false
	modelF := fs.StringP("model", "m", "", "model to use (overrides config)")
	taskF := fs.StringP("task", "t", "", "task/problem statement; '-' reads stdin")
	costF := fs.Float64P("cost-limit", "c", defaultCostLimit, "cost limit in $ (0 disables)")
	stepsF := fs.IntP("step-limit", "s", defaultStepLimit, "max number of steps")
	yoloF := fs.BoolP("yolo", "y", false, "run in yolo mode (execute without confirmation)")
	humanF := fs.BoolP("human", "H", false, "start in human mode (you type the commands)")
	whitelistF := fs.StringSlice("whitelist", nil, "regex whitelist of commands that skip confirmation (confirm mode)")
	outputF := fs.StringP("output", "o", "", "write the run to this JSON file")
	exitNowF := fs.Bool("exit-immediately", false, "don't confirm when the agent wants to finish")
	configWizF := fs.Bool("config", false, "run the configuration wizard and exit")
	versionF := fs.Bool("version", false, "print version and exit")
	fs.Parse(os.Args[1:])

	if *versionF {
		fmt.Println("stubbs dev")
		return nil
	}

	// First-run wizard (or forced with --config). Finally wires up
	// IsConfigured/SaveConfig.
	if *configWizF || !config.IsConfigured() {
		if err := runWizard(); err != nil {
			return err
		}
		if *configWizF {
			return nil
		}
	}
	cfg := config.Load()
	if cfg.APIKey == "" {
		return errors.New("no API key configured — run `stubbs --config` or set STUBBS_API_KEY")
	}
	model := orDefault(*modelF, cfg.Model)

	task := strings.TrimSpace(*taskF)
	if task == "" && fs.NArg() > 0 {
		task = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if task == "-" || (task == "" && !stdinIsTTY()) {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read task from stdin: %w", err)
		}
		task = strings.TrimSpace(string(b))
	}

	mode := agent.ModeConfirm
	switch {
	case *yoloF:
		mode = agent.ModeYolo
	case *humanF:
		mode = agent.ModeHuman
	}
	registry := types.NewRegistry()
	registry.Register(tools.NewBashTool())
	client := llm.NewORClient(cfg.APIKey, []string{model}, registry)
	environ := env.NewLocalEnvironment(env.EnvironmentConfig{Timeout: 300}, registry)

	iCfg := agent.InteractiveConfig{
		AgentConfig: agent.AgentConfig{
			StepLimit: *stepsF,
			CostLimit: float32(*costF),
		},
		Mode:             mode,
		WhitelistActions: *whitelistF,
		ConfirmExit:      mode == agent.ModeConfirm && !*exitNowF,
	}

	app := tui.New(tui.Options{Model: model})
	ia, err := agent.NewInteractiveAgent(iCfg, client, environ, model, app)
	if err != nil {
		return err
	}
	app.SetInterrupt(ia.Interrupt)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var (
		wg     sync.WaitGroup
		out    string
		runErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		runTask := task
		if runTask == "" {
			t, err := app.AwaitTask()
			if err != nil {
				app.Finish("", err)
				return
			}
			if strings.TrimSpace(t) == "" {
				app.Finish("", agent.ErrAborted)
				return
			}
			runTask = t
		}
		out, runErr = ia.Run(ctx, runTask)
		app.Finish(out, runErr)
		if *outputF != "" {
			writeRun(*outputF, ia, out, runErr)
		}
	}()

	tuiErr := app.Run()
	stop()
	wg.Wait()
	if tuiErr != nil {
		return tuiErr
	}
	switch {
	case runErr == nil, errors.Is(runErr, agent.ErrAborted):
		return nil
	case errors.Is(runErr, agent.ErrLimitsExceeded):
		os.Exit(2)
	}
	return runErr
}

func runWizard() error {
	cfg := config.Load()
	wasConfigured := config.IsConfigured()
	newCfg, err := tui.RunWizard(cfg, cfg.APIKey != "")
	if err != nil {
		return err
	}
	values := map[string]string{
		"PROVIDER": newCfg.Provider,
		"MODEL":    newCfg.Model,
		"ENV":      newCfg.Env,
	}
	if newCfg.APIKey != "" {
		values["API_KEY"] = newCfg.APIKey
	}
	if err := config.SaveConfig(config.ProjectConfigFile, values); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if wasConfigured {
		fmt.Println("Config updated:", config.ProjectConfigFile)
	} else {
		fmt.Println("Saved config to", config.ProjectConfigFile)
	}
	return nil
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// writeRun saves a minimal trajectory artifact; the full versioned format is
// a separate gap-analysis item.
func writeRun(path string, a *agent.InteractiveAgent, submission string, runErr error) {
	doc := struct {
		TrajectoryFormat string          `json:"trajectory_format"`
		Model            string          `json:"model"`
		Steps            int             `json:"steps"`
		ModelCalls       int             `json:"model_calls"`
		Cost             float32         `json:"cost"`
		Submission       string          `json:"submission"`
		Error            string          `json:"error,omitempty"`
		Messages         []types.Message `json:"messages"`
	}{
		TrajectoryFormat: "stubbs-trajectory-v1",
		Model:            a.Model,
		Steps:            a.Steps,
		ModelCalls:       a.ModelCalls,
		Cost:             a.Cost,
		Submission:       submission,
		Messages:         a.Messages,
	}
	if runErr != nil {
		doc.Error = runErr.Error()
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "stubbs: marshal output:", err)
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "stubbs: write output:", err)
	}
}

func orDefault(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
func oldmain() {

	// cfg := config.Load()
	// fmt.Println(cfg)
	// registry := types.NewRegistry()
	// bashTool := tools.NewBashTool()
	// registry.Register(bashTool)
	// client := llm.NewORClient(cfg.APIKey, []string{cfg.Model}, registry)
	// agentConfig := &agent.AgentConfig{
	// 	StepLimit: 24,
	// 	CostLimit: 5,
	// }
	// env := env.NewLocalEnvironment(env.EnvironmentConfig{Timeout: 300}, registry)
	// a, err := agent.NewAgent(agentConfig, client, env, cfg.Model)
	// if err != nil {
	// 	panic(err)
	// }
	// ctx := context.Background()
	// output, err := a.Run(ctx, "GIve me a snapshot of what processes are running on my machine, whats using a lot of CPU and RAM")
	// if err != nil {
	// 	panic(err)
	// }
	// fmt.Println(output)
}
