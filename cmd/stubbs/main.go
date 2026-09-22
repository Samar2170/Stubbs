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
	client := llm.NewORClient(cfg.APIKey, []string{cfg.Model}, registry)
	agentConfig := &agent.AgentConfig{
		StepLimit: 24,
		CostLimit: 5,
	}
	env := env.NewLocalEnvironment(env.EnvironmentConfig{Timeout: 300}, registry)
	a, err := agent.NewAgent(agentConfig, client, env, cfg.Model)
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	output, err := a.Run(ctx, "GIve me a snapshot of what processes are running on my machine, whats using a lot of CPU and RAM")
	if err != nil {
		panic(err)
	}
	fmt.Println(output)
}
