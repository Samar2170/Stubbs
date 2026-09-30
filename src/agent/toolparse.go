package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"stubbs/src/types"
)

// Some models (notably on OpenRouter) answer with tool-call markup embedded in
// the message content instead of the provider's native tool_calls field. The
// harness still expects structured calls, so recover the common formats here.
//
// Supported:
//
//	<function_calls><invoke name="file_edit">
//	  <parameter name="file_path" string="true">src/x.go</parameter>
//	</invoke></function_calls>
//
//	<tool_call>{"name":"bash","arguments":{"command":"ls"}}</tool_call>

var (
	invokeRe   = regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"\s*>(.*?)</invoke>`)
	paramRe    = regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)"(?:\s+string="(true|false)")?\s*>(.*?)</parameter>`)
	toolCallRe = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)
	markupRe   = regexp.MustCompile(`(?s)<(?:function_calls|tool_calls|invoke|tool_call|parameter)\b|\s+string="(?:true|false)">`)
	wrapperRe  = regexp.MustCompile(`(?s)</?(?:function_calls|tool_calls)>`)
)

// looksLikeToolMarkup reports whether content contains tool-call markup that
// the parser should have handled.
func looksLikeToolMarkup(content string) bool {
	return markupRe.MatchString(content)
}

// parseToolCalls recovers tool calls from assistant content. It returns the
// parsed calls, the content with the recognized markup removed, and whether
// anything was parsed.
func parseToolCalls(content string) (calls []types.ToolCall, stripped string, ok bool) {
	stripped = content
	addCall := func(name string, args map[string]any) {
		argsJSON, err := json.Marshal(args)
		if err != nil {
			return
		}
		calls = append(calls, types.ToolCall{
			ID:   fmt.Sprintf("call_parsed_%d", len(calls)+1),
			Type: "function",
			Function: types.FunctionCall{
				Name:      name,
				Arguments: string(argsJSON),
			},
		})
	}

	for _, m := range invokeRe.FindAllStringSubmatch(content, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		args := map[string]any{}
		for _, p := range paramRe.FindAllStringSubmatch(m[2], -1) {
			key := strings.TrimSpace(p[1])
			if key == "" {
				continue
			}
			args[key] = paramValue(p[3], p[2] == "false")
		}
		addCall(name, args)
		stripped = strings.Replace(stripped, m[0], "", 1)
	}

	for _, m := range toolCallRe.FindAllStringSubmatch(content, -1) {
		var raw struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(m[1]), &raw) != nil || strings.TrimSpace(raw.Name) == "" {
			continue
		}
		argStr := "{}"
		if len(raw.Arguments) > 0 {
			var asString string
			if json.Unmarshal(raw.Arguments, &asString) == nil {
				argStr = asString
			} else {
				argStr = string(raw.Arguments)
			}
		}
		calls = append(calls, types.ToolCall{
			ID:   fmt.Sprintf("call_parsed_%d", len(calls)+1),
			Type: "function",
			Function: types.FunctionCall{
				Name:      strings.TrimSpace(raw.Name),
				Arguments: argStr,
			},
		})
		stripped = strings.Replace(stripped, m[0], "", 1)
	}

	stripped = wrapperRe.ReplaceAllString(stripped, "")
	return calls, strings.TrimSpace(stripped), len(calls) > 0
}

// paramValue normalizes an XML parameter value. Anthropic marks non-string
// parameters with string="false"; those are parsed as JSON so booleans and
// numbers survive the round-trip. Everything else stays a string.
func paramValue(value string, nonString bool) any {
	if !nonString {
		return value
	}
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(value)), &v) == nil {
		return v
	}
	return value
}
