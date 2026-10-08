package agent

import (
	"errors"
	"testing"

	"stubbs/src/types"
)

type stubUI struct {
	askNewLimits func(curSteps, stepLimit int, curCost, costLimit float32) (steps int, cost float32, ok bool, err error)
	askConfirm   func(commands []string) (string, error)
}

func (s *stubUI) Info(format string, args ...any)                     {}
func (s *stubUI) Assistant(step int, cost float32, msg types.Message) {}
func (s *stubUI) Observation(call types.ToolCall, out types.ExecutionOutput) {
}
func (s *stubUI) Status(text string)        {}
func (s *stubUI) ModeChanged(m Mode)        {}
func (s *stubUI) ModelChanged(model string) {}
func (s *stubUI) AskConfirm(commands []string) (string, error) {
	if s.askConfirm != nil {
		return s.askConfirm(commands)
	}
	return "", nil
}
func (s *stubUI) AskCommand() (string, error) { return "", nil }
func (s *stubUI) AskComment() (string, error) { return "", nil }
func (s *stubUI) AskExit() (string, error)    { return "", nil }
func (s *stubUI) AskNewLimits(curSteps, stepLimit int, curCost, costLimit float32) (int, float32, bool, error) {
	if s.askNewLimits != nil {
		return s.askNewLimits(curSteps, stepLimit, curCost, costLimit)
	}
	return 0, 0, false, nil
}

func TestConfirmCallsSkipsSandboxedBash(t *testing.T) {
	asked := false
	ui := &stubUI{askConfirm: func([]string) (string, error) {
		asked = true
		return "", nil
	}}
	ia := &InteractiveAgent{cfg: InteractiveConfig{Mode: ModeConfirm, BashConfined: true}, ui: ui}
	calls := []types.ToolCall{{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}}}

	ok, err := ia.confirmCalls(calls)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("sandboxed bash should be approved")
	}
	if asked {
		t.Fatal("sandboxed bash should not prompt for confirmation")
	}
}

func TestConfirmCallsPromptsWhenUnconfined(t *testing.T) {
	asked := false
	ui := &stubUI{askConfirm: func([]string) (string, error) {
		asked = true
		return "", nil
	}}
	ia := &InteractiveAgent{cfg: InteractiveConfig{Mode: ModeConfirm, BashConfined: false}, ui: ui}
	calls := []types.ToolCall{{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}}}

	ok, err := ia.confirmCalls(calls)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("empty confirmation should approve")
	}
	if !asked {
		t.Fatal("unconfined bash should prompt for confirmation")
	}
}

func TestCheckBudgetAutoQuitSkipsPrompt(t *testing.T) {
	cfg := InteractiveConfig{AgentConfig: AgentConfig{StepLimit: 1, CostLimit: 1}, AutoQuit: true}
	ia := &InteractiveAgent{cfg: cfg, ui: &stubUI{}}
	ia.Agent = &Agent{config: &ia.cfg.AgentConfig, Steps: 1, Cost: 2}
	if err := ia.checkBudget(); !errors.Is(err, ErrLimitsExceeded) {
		t.Fatalf("want ErrLimitsExceeded, got %v", err)
	}
}

func TestCommandOf(t *testing.T) {
	cases := []struct {
		name string
		call types.ToolCall
		want string
	}{
		{
			name: "bash command",
			call: types.ToolCall{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"ls -la"}`}},
			want: "ls -la",
		},
		{
			name: "unknown falls back to raw",
			call: types.ToolCall{Function: types.FunctionCall{Name: "mystery", Arguments: `{"x":1}`}},
			want: `{"x":1}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CommandOf(tc.call); got != tc.want {
				t.Fatalf("CommandOf = %q, want %q", got, tc.want)
			}
		})
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
