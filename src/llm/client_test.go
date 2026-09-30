package llm

import "testing"

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
