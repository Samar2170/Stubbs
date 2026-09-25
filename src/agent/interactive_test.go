package agent

import (
	"errors"
	"testing"

	"stubbs/src/types"
)

type stubUI struct {
	askNewLimits func(curSteps, stepLimit int, curCost, costLimit float32) (steps int, cost float32, ok bool, err error)
}

func (s *stubUI) Info(format string, args ...any)                     {}
func (s *stubUI) Assistant(step int, cost float32, msg types.Message) {}
func (s *stubUI) Observation(call types.ToolCall, out types.ExecutionOutput) {
}
func (s *stubUI) Status(text string)                           {}
func (s *stubUI) ModeChanged(m Mode)                           {}
func (s *stubUI) AskConfirm(commands []string) (string, error) { return "", nil }
func (s *stubUI) AskCommand() (string, error)                  { return "", nil }
func (s *stubUI) AskComment() (string, error)                  { return "", nil }
func (s *stubUI) AskExit() (string, error)                     { return "", nil }
func (s *stubUI) AskNewLimits(curSteps, stepLimit int, curCost, costLimit float32) (int, float32, bool, error) {
	if s.askNewLimits != nil {
		return s.askNewLimits(curSteps, stepLimit, curCost, costLimit)
	}
	return 0, 0, false, nil
}

func TestCheckBudgetAutoQuitSkipsPrompt(t *testing.T) {
	cfg := InteractiveConfig{AgentConfig: AgentConfig{StepLimit: 1, CostLimit: 1}, AutoQuit: true}
	ia := &InteractiveAgent{cfg: cfg, ui: &stubUI{}}
	ia.Agent = &Agent{config: &ia.cfg.AgentConfig, Steps: 1, Cost: 2}
	if err := ia.checkBudget(); !errors.Is(err, ErrLimitsExceeded) {
		t.Fatalf("want ErrLimitsExceeded, got %v", err)
	}
}

func TestCheckBudgetPromptsForLimits(t *testing.T) {
	asked := false
	ui := &stubUI{
		askNewLimits: func(curSteps, stepLimit int, curCost, costLimit float32) (int, float32, bool, error) {
			asked = true
			return stepLimit * 10, costLimit * 10, true, nil
		},
	}
	cfg := InteractiveConfig{AgentConfig: AgentConfig{StepLimit: 1, CostLimit: 1}}
	ia := &InteractiveAgent{cfg: cfg, ui: ui}
	ia.Agent = &Agent{config: &ia.cfg.AgentConfig, Steps: 1, Cost: 2}
	if err := ia.checkBudget(); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if !asked {
		t.Fatal("expected AskNewLimits to be called")
	}
}
