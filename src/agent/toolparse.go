package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"

	"stubbs/src/types"
)

// Tool-call transport. Most providers support native function calling, so the
// harness sends tool definitions and reads structured tool_calls. Some models
// do not, and answer with tool-call markup in the message body instead. Rather
// than fabricating native calls from that text, the agent detects the
// mismatch, drops to a text protocol for the session, and parses the
// documented fenced format from then on.
//
// Text protocol format:
//
//	```stubbs-tool
//	{"name": "bash", "arguments": {"command": "ls"}}
//	```
//
// A single block may also contain a JSON array of call objects.

var (
	// markupRe recognizes tool-call markup that is not the text protocol, used
	// only to detect that the model ignored native function calling.
	markupRe = regexp.MustCompile(`(?s)<(?:function_calls|tool_calls|invoke|tool_call|parameter)\b|\s+string="(?:true|false)">`)

	textToolRe = regexp.MustCompile("(?s)```stubbs-tool[^\\n]*\\n(.*?)```")
)

var textCallSeq atomic.Uint64

func nextTextCallID() string {
	return fmt.Sprintf("text_call_%d", textCallSeq.Add(1))
}

// looksLikeToolMarkup reports whether content contains tool-call markup that is
// neither native tool_calls nor the text protocol.
func looksLikeToolMarkup(content string) bool {
	return markupRe.MatchString(content)
}

// parseTextToolCalls extracts calls from the text protocol. sawMarkup is true
// when a stubbs-tool block was present even if its body was not valid JSON.
func parseTextToolCalls(content string) (calls []types.ToolCall, stripped string, sawMarkup bool) {
	stripped = content
	for _, m := range textToolRe.FindAllStringSubmatch(content, -1) {
		sawMarkup = true
		parsed, ok := decodeTextCalls(strings.TrimSpace(m[1]))
		if !ok {
			continue
		}
		calls = append(calls, parsed...)
		stripped = strings.Replace(stripped, m[0], "", 1)
	}
	return calls, strings.TrimSpace(stripped), sawMarkup
}

type textCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func decodeTextCalls(block string) ([]types.ToolCall, bool) {
	var raw []textCall
	if err := json.Unmarshal([]byte(block), &raw); err != nil {
		var one textCall
		if err2 := json.Unmarshal([]byte(block), &one); err2 != nil {
			return nil, false
		}
		raw = []textCall{one}
	}
	out := make([]types.ToolCall, 0, len(raw))
	for _, c := range raw {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		argStr := "{}"
		if len(c.Arguments) > 0 {
			var asString string
			if json.Unmarshal(c.Arguments, &asString) == nil {
				argStr = asString
			} else {
				argStr = string(c.Arguments)
			}
		}
		out = append(out, types.ToolCall{
			ID:       nextTextCallID(),
			Type:     "function",
			Function: types.FunctionCall{Name: name, Arguments: argStr},
		})
	}
	return out, len(out) > 0
}

// textToolInstruction explains the text protocol to a model that cannot use
// native function calling.
func textToolInstruction(names []string) string {
	var b strings.Builder
	b.WriteString("Native function calling is unavailable for this model, so a text tool protocol is in use. To run a tool, reply with one fenced block:\n\n")
	b.WriteString("```stubbs-tool\n{\"name\": \"<tool>\", \"arguments\": {\"<arg>\": \"<value>\"}}\n```\n\n")
	b.WriteString("A block may contain a JSON array of several call objects. ")
	if len(names) > 0 {
		fmt.Fprintf(&b, "Available tools: %s. Use only those names. ", strings.Join(names, ", "))
	}
	b.WriteString("Never emit XML or any other tool-call format. When you are done, reply normally with no block.")
	return b.String()
}
