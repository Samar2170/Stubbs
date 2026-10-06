package types

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeTool struct{ name string }

func (f fakeTool) Name() string                { return f.name }
func (f fakeTool) Description() string         { return "" }
func (f fakeTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (f fakeTool) Execute(context.Context, string) ExecutionOutput {
	return ExecutionOutput{}
}

func TestRegistryHas(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "bash"})
	if !r.Has("bash") {
		t.Fatal("registered tool should be reported present")
	}
	if r.Has("read") {
		t.Fatal("unregistered tool should be reported absent")
	}
	var nilReg *Registry
	if nilReg.Has("bash") {
		t.Fatal("nil registry should report absent")
	}
}
