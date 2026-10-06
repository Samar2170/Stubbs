package agent

import (
	"errors"
	"regexp"
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

func TestCheckBudgetAutoQuitSkipsPrompt(t *testing.T) {
	cfg := InteractiveConfig{AgentConfig: AgentConfig{StepLimit: 1, CostLimit: 1}, AutoQuit: true}
	ia := &InteractiveAgent{cfg: cfg, ui: &stubUI{}}
	ia.Agent = &Agent{config: &ia.cfg.AgentConfig, Steps: 1, Cost: 2}
	if err := ia.checkBudget(); !errors.Is(err, ErrLimitsExceeded) {
		t.Fatalf("want ErrLimitsExceeded, got %v", err)
	}
}

func TestCommandOfFileTools(t *testing.T) {
	cases := []struct {
		name string
		call types.ToolCall
		want string
	}{
		{
			name: "read",
			call: types.ToolCall{Function: types.FunctionCall{Name: "read", Arguments: `{"file_path":"a.go"}`}},
			want: "read a.go",
		},
		{
			name: "write",
			call: types.ToolCall{Function: types.FunctionCall{Name: "write", Arguments: `{"file_path":"a.go","content":"hi"}`}},
			want: "write a.go (2 bytes)",
		},
		{
			name: "append",
			call: types.ToolCall{Function: types.FunctionCall{Name: "write", Arguments: `{"file_path":"a.go","content":"hi","append":true}`}},
			want: "append a.go (2 bytes)",
		},
		{
			name: "list default",
			call: types.ToolCall{Function: types.FunctionCall{Name: "list", Arguments: `{}`}},
			want: "list .",
		},
		{
			name: "list recursive",
			call: types.ToolCall{Function: types.FunctionCall{Name: "list", Arguments: `{"path":"src","recursive":true}`}},
			want: "list src (recursive)",
		},
		{
			name: "edit",
			call: types.ToolCall{Function: types.FunctionCall{Name: "edit", Arguments: `{"file_path":"a.go"}`}},
			want: "edit a.go",
		},
		{
			name: "bash falls through",
			call: types.ToolCall{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"ls -la"}`}},
			want: "ls -la",
		},
		{
			name: "webfetch",
			call: types.ToolCall{Function: types.FunctionCall{Name: "webfetch", Arguments: `{"url":"https://example.com"}`}},
			want: "fetch https://example.com",
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

func TestConfirmCallsRespectsApproval(t *testing.T) {
	bashCall := types.ToolCall{Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}}
	readCall := types.ToolCall{Function: types.FunctionCall{Name: "read", Arguments: `{"file_path":"a.go"}`}}

	cases := []struct {
		name         string
		mode         Mode
		approval     map[string]string
		whitelist    []string
		inWorkdir    bool
		bashConfined bool
		calls        []types.ToolCall
		wantConfirm  bool
	}{
		{
			name:        "approved tool skips prompt",
			mode:        ModeConfirm,
			approval:    map[string]string{"read": "allow"},
			calls:       []types.ToolCall{readCall},
			wantConfirm: false,
		},
		{
			name:        "unlisted tool prompts",
			mode:        ModeConfirm,
			approval:    map[string]string{"read": "allow"},
			calls:       []types.ToolCall{bashCall},
			wantConfirm: true,
		},
		{
			name:        "explicit confirm prompts",
			mode:        ModeConfirm,
			approval:    map[string]string{"bash": "confirm"},
			calls:       []types.ToolCall{bashCall},
			wantConfirm: true,
		},
		{
			name:        "yolo skips all",
			mode:        ModeYolo,
			calls:       []types.ToolCall{bashCall},
			wantConfirm: false,
		},
		{
			name:        "whitelist still skips prompt",
			mode:        ModeConfirm,
			whitelist:   []string{`^ls`},
			calls:       []types.ToolCall{bashCall},
			wantConfirm: false,
		},
		{
			name:         "in_workdir skips prompt for confined bash",
			mode:         ModeConfirm,
			inWorkdir:    true,
			bashConfined: true,
			calls:        []types.ToolCall{bashCall},
			wantConfirm:  false,
		},
		{
			name:        "in_workdir still prompts for unconfined bash",
			mode:        ModeConfirm,
			inWorkdir:   true,
			calls:       []types.ToolCall{bashCall},
			wantConfirm: true,
		},
		{
			name:        "in_workdir skips prompt for file tools",
			mode:        ModeConfirm,
			inWorkdir:   true,
			calls:       []types.ToolCall{readCall},
			wantConfirm: false,
		},
		{
			name:        "in_workdir off prompts for file tools",
			mode:        ModeConfirm,
			calls:       []types.ToolCall{readCall},
			wantConfirm: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			ui := &stubUI{askConfirm: func(commands []string) (string, error) {
				asked = true
				return "", nil
			}}
			ia := &InteractiveAgent{
				cfg: InteractiveConfig{Mode: tc.mode, Approval: tc.approval, InWorkdir: tc.inWorkdir, BashConfined: tc.bashConfined},
				ui:  ui,
			}
			ia.Agent = &Agent{config: &ia.cfg.AgentConfig}
			for _, pattern := range tc.whitelist {
				re, err := regexp.Compile(pattern)
				if err != nil {
					t.Fatal(err)
				}
				ia.whitelist = append(ia.whitelist, re)
			}

			approved, err := ia.confirmCalls(tc.calls)
			if err != nil {
				t.Fatalf("confirmCalls: %v", err)
			}
			if !approved {
				t.Fatal("expected calls to be approved")
			}
			if asked != tc.wantConfirm {
				t.Fatalf("AskConfirm called = %v, want %v", asked, tc.wantConfirm)
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
