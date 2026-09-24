package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"stubbs/src/config"
)

var ErrWizardAborted = errors.New("wizard: aborted")

type wizardStep struct {
	title    string
	password bool
}

type wizard struct {
	cfg     config.StubbsConfig
	step    int
	steps   []wizardStep
	input   textinput.Model
	values  []string
	hasKey  bool
	errMsg  string
	aborted bool
	width   int
	height  int
}

func newWizard(cfg config.StubbsConfig, hasKeyFromEnv bool) wizard {
	steps := []wizardStep{
		{title: "OpenRouter API key", password: true},
		{title: "Model"},
		{title: "Environment"},
	}
	ti := textinput.New()
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 50
	w := wizard{
		cfg:    cfg,
		steps:  steps,
		input:  ti,
		values: make([]string, len(steps)),
		hasKey: hasKeyFromEnv,
	}
	w.applyStep(0)
	return w
}

func (w *wizard) applyStep(i int) {
	w.input.Reset()
	w.input.EchoMode = textinput.EchoNormal
	w.input.Placeholder = ""
	switch i {
	case 0:
		w.input.EchoMode = textinput.EchoPassword
		w.input.EchoCharacter = '•'
		w.input.Placeholder = "sk-or-… (enter keeps the env value)"
	case 1:
		w.input.Placeholder = config.DefaultModel
		w.input.SetValue(w.cfg.Model)
	case 2:
		w.input.Placeholder = "local"
		w.input.SetValue(w.cfg.Env)
	}
	w.input.Focus()
}

func (w wizard) Init() tea.Cmd { return textinput.Blink }

func (w wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		return w, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			w.aborted = true
			return w, tea.Quit
		case "enter":
			value := strings.TrimSpace(w.input.Value())
			if !w.valid(value) {
				return w, nil
			}
			w.values[w.step] = value
			if w.step == len(w.steps)-1 {
				return w, tea.Quit
			}
			w.step++
			w.errMsg = ""
			w.applyStep(w.step)
			return w, textinput.Blink
		}
	}
	var cmd tea.Cmd
	w.input, cmd = w.input.Update(msg)
	return w, cmd
}

func (w wizard) valid(value string) bool {
	switch w.step {
	case 0:
		if value == "" && !w.hasKey {
			w.errMsg = "API key is required (or set STUBBS_API_KEY / OPENROUTER_API_KEY)."
			return false
		}
	case 1:
		if value == "" {
			w.errMsg = "Model is required."
			return false
		}
	case 2:
		if value == "" {
			w.errMsg = "Environment is required."
			return false
		}
	}
	return true
}

func (w wizard) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Welcome to stubbs!") + "\n")
	b.WriteString(dimStyle.Render("Configure OpenRouter access. Saved to " + config.ProjectConfigFile + "\n\n"))
	fmt.Fprintf(&b, "[%d/%d] %s\n", w.step+1, len(w.steps), w.steps[w.step].title)
	b.WriteString(w.input.View() + "\n")
	if w.errMsg != "" {
		b.WriteString(infoStyle.Render(w.errMsg) + "\n")
	}
	b.WriteString(dimStyle.Render("\nenter next · esc abort"))
	return b.String()
}

// RunWizard runs the first-run configuration wizard. Returns
// ErrWizardAborted if the user quit early.
func RunWizard(cfg config.StubbsConfig, hasKeyFromEnv bool) (config.StubbsConfig, error) {
	p := tea.NewProgram(newWizard(cfg, hasKeyFromEnv))
	m, err := p.Run()
	if err != nil {
		return cfg, err
	}
	w, ok := m.(wizard)
	if !ok || w.aborted {
		return cfg, ErrWizardAborted
	}
	out := cfg
	if v := w.values[0]; v != "" {
		out.APIKey = v
	}
	if v := w.values[1]; v != "" {
		out.Model = v
	}
	if v := w.values[2]; v != "" {
		out.Env = v
	}
	out.Provider = "openrouter"
	return out, nil
}
