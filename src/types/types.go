package types

import "encoding/json"

type ToolDefinition struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

type FunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name string `json:"name"`
	// Arguments is the JSON-encoded argument string, e.g. {"command":"ls -la"}.
	Arguments string `json:"arguments"`
}
