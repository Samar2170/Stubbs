package config

import (
	"os"
	"testing"
)

func TestMemoryConfigDefaultsAndOverride(t *testing.T) {
	isolate(t)

	cfg := Load()
	if !cfg.Memory.Enabled || !cfg.Memory.AutoSummarize || !cfg.Memory.AutoRepoMap || cfg.Memory.TopK != 5 {
		t.Fatalf("defaults = %+v", cfg.Memory)
	}
	if cfg.Memory.BudgetTokens != 0 {
		t.Fatalf("budget default = %d, want 0 (no cap)", cfg.Memory.BudgetTokens)
	}

	yaml := "provider: openrouter\nenv: local\ntheme: tokyo\nmemory:\n  enabled: false\n  budget_tokens: 4096\n"
	if err := os.WriteFile(ProjectConfigFile, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg = Load()
	if cfg.Memory.Enabled {
		t.Fatal("enabled override not applied")
	}
	if cfg.Memory.BudgetTokens != 4096 {
		t.Fatalf("budget = %d, want 4096", cfg.Memory.BudgetTokens)
	}
	if !cfg.Memory.AutoSummarize {
		t.Fatal("unset auto_summarize should keep its default")
	}
	if cfg.Memory.TopK != 5 {
		t.Fatalf("top_k = %d, want default 5", cfg.Memory.TopK)
	}
}
