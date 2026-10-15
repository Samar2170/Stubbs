package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"stubbs/src/agent"
	"stubbs/src/config"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/tools"
	"stubbs/src/tui"
	"stubbs/src/types"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/pflag"
)

const (
	defaultStepLimit = 50
	defaultCostLimit = 1.0
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stubbs-chat:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := pflag.NewFlagSet("stubbs-chat", pflag.ExitOnError)
	fs.SortFlags = false
	modelF := fs.StringP("model", "m", "", "model to use (overrides config)")
	costF := fs.Float64P("cost-limit", "c", defaultCostLimit, "cost limit in $ (0 disables)")
	stepsF := fs.IntP("step-limit", "s", defaultStepLimit, "max model steps per turn (0 disables)")
	configWizF := fs.Bool("config", false, "run the configuration wizard and exit")
	versionF := fs.Bool("version", false, "print version and exit")
	fs.Parse(os.Args[1:])

	if *versionF {
		fmt.Println("stubbs-chat dev")
		return nil
	}

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
	model := orDefault(orDefault(*modelF, cfg.ActiveModel()), config.DefaultModel)

	// The chat binary is intentionally minimal: it only has the web tool.
	registry := types.NewRegistry()
	registry.Register(tools.NewWebTool())

	workdir, err := os.Getwd()
	if err != nil {
		workdir = "."
	}
	environ := env.NewLocalEnvironment(env.EnvironmentConfig{WorkingDir: workdir, Timeout: 120}, registry)
	client := llm.NewORClient(cfg.APIKey, []string{model}, registry)

	app := tui.NewChat(tui.ChatOptions{Model: model, Theme: cfg.Theme})
	chat, err := agent.NewChat(agent.ChatConfig{
		StepLimit: *stepsF,
		CostLimit: float32(*costF),
	}, client, environ, model, app)
	if err != nil {
		return err
	}
	app.SetInterrupt(chat.Interrupt)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			line, err := app.NextInput()
			if err != nil {
				app.Quit()
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			_, err = chat.Send(ctx, line)
			if errors.Is(err, context.Canceled) {
				err = nil
			}
			app.TurnDone(err)
		}
	}()

	tuiErr := app.Run()
	stop()
	wg.Wait()
	chat.Close()
	if tuiErr != nil && !errors.Is(tuiErr, tea.ErrInterrupted) && !errors.Is(tuiErr, tea.ErrProgramKilled) {
		return tuiErr
	}
	return nil
}

func runWizard() error {
	cfg := config.Load()
	wasConfigured := config.IsConfigured()
	newCfg, err := tui.RunWizard(cfg, cfg.APIKey != "")
	if err != nil {
		return err
	}
	if err := config.SaveConfig(newCfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if wasConfigured {
		fmt.Println("Config updated:", config.ProjectConfigFile)
	} else {
		fmt.Println("Saved config to", config.ProjectConfigFile)
	}
	return nil
}

func orDefault(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
