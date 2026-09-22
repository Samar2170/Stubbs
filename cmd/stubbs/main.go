package main

import (
	"context"
	"fmt"
	"stubbs/src/agent"
	"stubbs/src/config"
	"stubbs/src/env"
	"stubbs/src/llm"
	"stubbs/src/tools"
	"stubbs/src/types"
)

func main() {
	cfg := config.Load()
	fmt.Println(cfg)
	registry := types.NewRegistry()
	bashTool := tools.NewBashTool()
	registry.Register(bashTool)
	client := llm.NewORClient(cfg.APIKey, []string{
		cfg.Model,
	},
		*registry,
	)

	agentConfig := &agent.AgentConfig{
		InstanceTemplate: "",
		StepLimit:        24,
		CostLimit:        5,
	}
	env := env.NewLocalEnvironment(".", "", 300)
	a := agent.NewAgent(agentConfig, []types.Tool{bashTool}, client, env, cfg.Model)
	ctx := context.Background()
	a.Run(ctx, "List files in the current working directory")
}
