package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"stubbs/src/types"
	"time"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

type ORClient struct {
	apiKey    string
	models    []string
	maxTokens int
	baseURL   string
	hc        *http.Client
	// Session   *Session
	Tools []types.Tool
}

type ORChatRequest struct {
	Model     string                 `json:"model"`
	Messages  []types.Message        `json:"messages"`
	MaxTokens int                    `json:"max_tokens,omitempty"`
	Tools     []types.ToolDefinition `json:"tools,omitempty"`
}

type chatUsage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float32 `json:"cost"`
}

// map[choices:[map[finish_reason:tool_calls index:0 logprobs:<nil> message:map[content:<nil> reasoning:The user wants me to list files in the current working directory. I'll use the bash tool to do this. reasoning_details:[map[format:unknown index:0 text:The user wants me to list files in the current working directory. I'll use the bash tool to do this. type:reasoning.text]] refusal:<nil> role:assistant tool_calls:[map[function:map[arguments:{"command":"ls -la"} name:bash] id:call_01a0c80d35917cb3931eebe5 index:0 type:function]]] native_finish_reason:tool_calls]] created:1.790062703e+09 id:gen-1790062703-I6zUKD8au7yLDyVVTKuK model:z-ai/glm-5.3-flash object:chat.completion provider:Together service_tier:<nil> system_fingerprint:default usage:map[completion_tokens:36 completion_tokens_details:map[audio_tokens:0 image_tokens:0 reasoning_tokens:23] cost:3.399e-05 cost_details:map[upstream_inference_completions_cost:1.8e-05 upstream_inference_cost:3.399e-05 upstream_inference_prompt_cost:1.599e-05] is_byok:false prompt_tokens:209 prompt_tokens_details:map[audio_tokens:0 cache_write_tokens:0 cached_tokens:128 video_tokens:0] total_tokens:245]]

type ORChatResponse struct {
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			Reasoning string           `json:"reasoning,omitempty"`
			ToolCalls []types.ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

func (cr *ORChatResponse) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Choices: %d\n", len(cr.Choices))
	for i, choice := range cr.Choices {
		fmt.Fprintf(&b, "Choice %d: %s\n", i, choice.Message.Content)
		fmt.Fprintf(&b, "Tool Calls %d\n", len(choice.Message.ToolCalls))
		for j, tc := range choice.Message.ToolCalls {
			fmt.Fprintf(&b, "  Tool Call %d: %s(%s)\n", j, tc.Function.Name, tc.Function.Arguments)
		}
	}
	if cr.Usage != nil {
		fmt.Fprintf(&b, "Usage: Prompt=%d, Completion=%d, Total=%d Cost=%f\n",
			cr.Usage.PromptTokens, cr.Usage.CompletionTokens, cr.Usage.TotalTokens, cr.Usage.Cost)
	}
	if cr.Error != nil {
		fmt.Fprintf(&b, "Error: %s (code %d)\n", cr.Error.Message, cr.Error.Code)
	}
	return b.String()
}

type Option func(*ORClient)

func WithTimeout(d time.Duration) Option {
	return func(c *ORClient) { c.hc.Timeout = d }
}

// WithBaseURL overrides the API endpoint (tests point this at a stub server).
func WithBaseURL(u string) Option {
	return func(c *ORClient) { c.baseURL = u }
}

func NewORClient(apiKey string, modelIDs []string, toolRegistry types.Registry, opts ...Option) *ORClient {
	if len(modelIDs) == 0 {
		modelIDs = []string{"z-ai/glm-5.3-flash"}
	}
	c := &ORClient{
		apiKey:  apiKey,
		models:  modelIDs,
		baseURL: defaultBaseURL,
		hc:      &http.Client{Timeout: 3 * time.Minute},
		Tools:   toolRegistry.List(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *ORClient) toolDefinitions() []types.ToolDefinition {
	if len(c.Tools) == 0 {
		return nil
	}
	defs := make([]types.ToolDefinition, 0, len(c.Tools))
	for _, t := range c.Tools {
		defs = append(defs, types.ToolDefinition{
			Type: "function",
			Function: types.FunctionSpec{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return defs
}

func (c *ORClient) CompleteText(ctx context.Context, messages []types.Message) (ORChatResponse, error) {
	if c == nil {
		return ORChatResponse{}, fmt.Errorf("client is nil")
	}
	resp, err := c.query(ctx, c.models[0], messages)
	if err != nil {
		return ORChatResponse{}, err
	}
	if len(resp.Choices) == 0 {
		return resp, fmt.Errorf("openrouter: response has no choices")
	}

	return resp, nil
}

func (c *ORClient) query(ctx context.Context, model string, messages []types.Message) (ORChatResponse, error) {
	var chatResp ORChatResponse
	toolDefs := c.toolDefinitions()
	payload, err := json.Marshal(ORChatRequest{
		Model: model, Messages: messages, MaxTokens: c.maxTokens, Tools: toolDefs,
	})
	if err != nil {
		return chatResp, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(payload))
	if err != nil {
		return chatResp, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.hc.Do(req)
	if err != nil {
		return chatResp, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return chatResp, fmt.Errorf("openrouter API error:%d %s", response.StatusCode, response.Status)
	}
	// debugResponse := make(map[string]interface{})
	// if err := json.NewDecoder(response.Body).Decode(&debugResponse); err != nil {
	// 	return chatResp, err
	// }
	// fmt.Println(debugResponse)

	var result ORChatResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return chatResp, err
	}
	return result, nil
}
