package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"stubbs/src/agent"
	"stubbs/src/config"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/memory"
	"stubbs/src/prompts"
	"stubbs/src/tools"
	"stubbs/src/tui"
	"stubbs/src/types"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/pflag"
)

const (
	defaultStepLimit = 24
	defaultCostLimit = 5.0
	// defaultLLMTimeout caps a single model request. Responses are not
	// streamed, so the whole body must arrive within this window; keep it
	// generous or long completions get killed mid-read.
	defaultLLMTimeout = 5 * time.Minute
	// summaryGrace is how long a clean run waits for its background memory
	// summary to persist before the process exits. It never delays the UI.
	summaryGrace = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stubbs:", err)
		os.Exit(1)
	}
}

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
	workdirF := fs.StringP("workdir", "C", "", "working directory the agent's tools are confined to (default: current directory)")
	readSecretsF := fs.Bool("read-secrets", false, "allow the agent to read .env/secret files")
	outputF := fs.StringP("output", "o", "", "write the run to this JSON file")
	exitNowF := fs.Bool("exit-immediately", false, "don't confirm when the agent wants to finish")
	autoQuitF := fs.Bool("auto-quit", false, "quit automatically when the run ends (benchmark mode; implies --exit-immediately)")
	configWizF := fs.Bool("config", false, "run the configuration wizard and exit")
	mapF := fs.Bool("map", false, "regenerate the repository memory map and exit")
	versionF := fs.Bool("version", false, "print version and exit")
	envTimeoutF := fs.Int("env-timeout", env.DefaultTimeout, "per-command tool timeout in seconds")
	llmTimeoutF := fs.Duration("llm-timeout", defaultLLMTimeout, "timeout for a single model request (0 disables)")
	contextBudgetF := fs.Int("context-budget", 0, "max context tokens sent per request (0 = config default)")
	maxCallsF := fs.Int("max-model-calls", 0, "hard cap on model requests per run (0 = no cap)")
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
	model := orDefault(orDefault(*modelF, cfg.ActiveModel()), config.DefaultModel)

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
	workdir := *workdirF
	if workdir == "" {
		if wd, err := os.Getwd(); err == nil {
			workdir = wd
		} else {
			workdir = "."
		}
	}
	if abs, err := filepath.Abs(workdir); err == nil {
		workdir = abs
	}

	var memStore *memory.Store
	if cfg.Memory.Enabled {
		store, err := memory.NewStore(memory.Options{
			Dir:               config.MemoryDir,
			BudgetTokens:      cfg.Memory.BudgetTokens,
			TopK:              cfg.Memory.TopK,
			AutoSummarize:     cfg.Memory.AutoSummarize,
			AutoRepoMap:       cfg.Memory.AutoRepoMap,
			CaptureHeuristics: cfg.Memory.CaptureHeuristics,
		})
		if err != nil {
			return err
		}
		memStore = store
	}

	registry := types.NewRegistry()
	bashTool := tools.NewBashTool()
	bashTool.Dir = workdir
	inWorkdir := cfg.Approval.InWorkdir
	bashConfined := false
	if inWorkdir && tools.SandboxAvailable("bwrap") {
		bashTool.Confine = true
		bashTool.SandboxHome = filepath.Join(config.ProjectDir, "sandbox-home")
		bashConfined = true
	}
	registry.Register(bashTool)
	readTool := tools.NewFileReadTool(workdir)
	readTool.ReadSecrets = *readSecretsF
	registry.Register(readTool)
	registry.Register(tools.NewFileWriteTool(workdir))
	registry.Register(tools.NewFileListTool(workdir))
	registry.Register(tools.NewFileEditTool(workdir))
	registry.Register(tools.NewSearchTool(workdir))
	registry.Register(tools.NewWebFetchTool())
	if memStore != nil {
		registry.Register(tools.NewMemoryTool(memStore))
	}
	client := llm.NewORClient(cfg.APIKey, []string{model}, registry, llm.WithTimeout(*llmTimeoutF))
	environ := env.NewLocalEnvironment(env.EnvironmentConfig{WorkingDir: workdir, Timeout: *envTimeoutF}, registry)

	toolNames := make([]string, 0)
	for _, t := range registry.List() {
		toolNames = append(toolNames, t.Name())
	}

	// The coding-agent policy comes first, then the harness rules (tool
	// listing, batching, stop conditions). Kept here so it can become a config
	// option later.
	systemPrompt := prompts.CodingAgent + "\n\n" + agent.SystemPromptFor(toolNames)

	contextBudget := cfg.ContextBudgetTokens
	if *contextBudgetF > 0 {
		contextBudget = *contextBudgetF
	}

	iCfg := agent.InteractiveConfig{
		AgentConfig: agent.AgentConfig{
			StepLimit:     *stepsF,
			CostLimit:     float32(*costF),
			MaxModelCalls: *maxCallsF,
			ContextBudget: contextBudget,
			WorkingDir:    workdir,
			SystemPrompt:  systemPrompt,
			ToolNames:     toolNames,
			Memory:        memStore,
		},
		Mode:             mode,
		WhitelistActions: *whitelistF,
		Approval:         cfg.Approval.Tools,
		InWorkdir:        inWorkdir,
		BashConfined:     bashConfined,
		ConfirmExit:      mode == agent.ModeConfirm && !*exitNowF,
		AutoQuit:         *autoQuitF,
	}

	app := tui.New(tui.Options{Model: model, AutoQuit: *autoQuitF, Theme: cfg.Theme, Mode: mode})
	ia, err := agent.NewInteractiveAgent(iCfg, client, environ, model, app)
	if err != nil {
		return err
	}
	app.SetInterrupt(ia.Interrupt)
	app.SetModelHandlers(func() ([]tui.ModelChoice, error) {
		mctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		models, err := client.ListModels(mctx)
		if err != nil {
			return nil, err
		}
		out := make([]tui.ModelChoice, len(models))
		for i, m := range models {
			out[i] = tui.ModelChoice{ID: m.ID, Name: m.Name}
		}
		return out, nil
	}, func(id string) error {
		ia.SetModel(id) // updates the client, the agent, the session and the header
		return config.SaveModel(id)
	})
	app.SetSessionHandlers(tui.SessionHandlers{
		List: func() ([]tui.SessionChoice, error) {
			infos, err := agent.ListSessions()
			if err != nil {
				return nil, err
			}
			out := make([]tui.SessionChoice, len(infos))
			for i, info := range infos {
				out[i] = tui.SessionChoice{
					ID:           info.ID,
					FirstMessage: info.FirstMessage,
					Updated:      info.Updated,
					Messages:     info.Messages,
				}
			}
			return out, nil
		},
		Open: func(id string) ([]types.Message, error) {
			session, msgs, err := agent.LoadSession(ia.ModelName(), id, systemPrompt)
			if err != nil {
				return nil, err
			}
			ia.ResumeSession(session)
			return msgs, nil
		},
	})
	if memStore != nil {
		app.SetMemoryHandlers(tui.MemoryHandlers{
			Remember: func(text string) error {
				title := strings.TrimSpace(text)
				if len(title) > 60 {
					title = title[:60]
				}
				return memStore.Add(memory.Entry{
					Kind:       memory.KindPreference,
					Title:      title,
					Body:       text,
					Importance: 0.8,
					Source:     "user",
				})
			},
			Forget: func(query string) (int, error) {
				entries, err := memStore.Load()
				if err != nil {
					return 0, err
				}
				q := strings.ToLower(query)
				n := 0
				for _, e := range entries {
					if e.ID == query || strings.Contains(strings.ToLower(e.Title+" "+e.Body), q) {
						if err := memStore.Delete(e.ID); err != nil {
							return n, err
						}
						n++
					}
				}
				return n, nil
			},
			List: func() ([]string, error) {
				entries, err := memStore.Load()
				if err != nil {
					return nil, err
				}
				out := make([]string, 0, len(entries))
				for _, e := range entries {
					out = append(out, fmt.Sprintf("%s (%s)", e.Title, e.ID))
				}
				return out, nil
			},
			Map: func() error {
				mctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				_, err := ia.GenerateRepoMap(mctx)
				return err
			},
		})
	}

	if *mapF {
		if memStore == nil {
			return errors.New("memory is disabled; cannot generate a repository map")
		}
		mctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, err := ia.GenerateRepoMap(mctx); err != nil {
			return err
		}
		fmt.Println("Wrote repository map to", filepath.Join(config.MemoryDir, "repo-map.md"))
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var (
		wg     sync.WaitGroup
		bg     sync.WaitGroup
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
				if errors.Is(err, agent.ErrInterrupted) {
					app.Quit()
					return
				}
				app.Finish("", err)
				return
			}
			if strings.TrimSpace(t) == "" {
				app.Finish("", agent.ErrAborted)
				return
			}
			runTask = t
		} else {
			// CLI-provided task: the TUI didn't echo it, show it now.
			app.ShowUser(runTask)
		}
		out, runErr = ia.Run(ctx, runTask)
		app.Finish(out, runErr)
		if *outputF != "" {
			writeRun(*outputF, ia, out, runErr)
		}
		// Summarize only a clean run, and off the critical path: the UI has
		// already received its completion message.
		if runErr == nil {
			bg.Add(1)
			go func() {
				defer bg.Done()
				ia.SummarizeMemory(context.Background())
			}()
		}
	}()

	tuiErr := app.Run()
	stop()
	wg.Wait()
	printSummary(ia, runErr)
	// Give a clean run's background summary a short grace period to persist
	// before the process exits; a quick quit never blocks the UI.
	summaryDone := make(chan struct{})
	go func() { bg.Wait(); close(summaryDone) }()
	select {
	case <-summaryDone:
	case <-time.After(summaryGrace):
	}
	// A user interrupt (ctrl-c delivered as SIGINT) or a killed program is a
	// normal way to leave the TUI, not an error.
	if tuiErr != nil && !errors.Is(tuiErr, tea.ErrInterrupted) && !errors.Is(tuiErr, tea.ErrProgramKilled) {
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
		Model:            a.ModelName(),
		Steps:            a.Steps,
		ModelCalls:       a.ModelCalls,
		Cost:             a.Cost,
		Submission:       submission,
		Messages:         a.Session.History(),
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

func printSummary(a *agent.InteractiveAgent, runErr error) {
	fmt.Fprintf(os.Stderr, "stubbs: %s (steps=%d, cost=$%.4f)\n", summarizeRun(runErr), a.Steps, a.Cost)
}

func summarizeRun(err error) string {
	switch {
	case err == nil:
		return "complete"
	case errors.Is(err, agent.ErrAborted):
		return "aborted"
	case errors.Is(err, agent.ErrInterrupted):
		return "interrupted"
	case errors.Is(err, agent.ErrLimitsExceeded):
		return "limits exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "failed"
	}
}
