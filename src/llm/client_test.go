package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestORClientSetModel(t *testing.T) {
	c := NewORClient("key", []string{"a/one"}, nil)
	if got := c.Model(); got != "a/one" {
		t.Fatalf("Model() = %q, want a/one", got)
	}
	c.SetModel("b/two")
	if got := c.Model(); got != "b/two" {
		t.Fatalf("Model() after SetModel = %q, want b/two", got)
	}
	c.SetModel("")
	if got := c.Model(); got != "b/two" {
		t.Fatalf("SetModel(\"\") should be ignored, got %q", got)
	}
}

func TestRetryable(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"rate limited", ctx, &APIError{Status: 429}, true},
		{"server error", ctx, &APIError{Status: 503}, true},
		{"bad request", ctx, &APIError{Status: 400}, false},
		{"timeout", ctx, context.DeadlineExceeded, true},
		{"plain error", ctx, errors.New("boom"), false},
		{"parent cancelled", cancelled, &APIError{Status: 503}, false},
	}
	for _, tc := range cases {
		if got := retryable(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: retryable = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A slow-but-live request should be retried; guard against a regression
	// where the parent context's own health is mistaken for the request's.
	tctx, tcancel := context.WithTimeout(ctx, time.Minute)
	defer tcancel()
	if !retryable(tctx, context.DeadlineExceeded) {
		t.Error("request timeout with a live parent context should be retryable")
	}
}

func TestORClientSetNativeTools(t *testing.T) {
	c := NewORClient("key", []string{"a/one"}, nil)
	if !c.sendTools {
		t.Fatal("native tools should be enabled by default")
	}
	c.SetNativeTools(false)
	if c.sendTools {
		t.Fatal("SetNativeTools(false) should disable tool definitions")
	}
	c.SetNativeTools(true)
	if !c.sendTools {
		t.Fatal("SetNativeTools(true) should re-enable tool definitions")
	}
}
