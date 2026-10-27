package tools

import (
	"context"
	"testing"
	"time"
)

func TestBashCancelKillsProcessTree(t *testing.T) {
	b := NewBashTool()
	b.Dir = t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	out := b.Execute(ctx, `{"command":"sleep 30 & wait"}`)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled command took %v, want < 1s", elapsed)
	}
	if out.Code == 0 {
		t.Fatalf("expected non-zero exit after cancellation, got %+v", out)
	}
}
