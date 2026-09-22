package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"stubbs/src/types"
	"time"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"
const maxAttempts = 3

type ORClient struct {
	apiKey    string
	models    []string
	maxTokens int
	baseURL   string
	hc        *http.Client
	Tools     []types.Tool
}

type ORChatRequest struct {
	Model     string                 `json:"model"`
	Messages  []types.Message        `json:"messages"`
	MaxTokens int                    `json:"max_tokens,omitempty"`
	Tools     []types.ToolDefinition `json:"tools,omitempty"`
}

type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float32 `json:"cost"`
}

type ResponseMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []types.ToolCall `json:"tool_calls,omitempty"`
}

type Choice struct {
	Message      ResponseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type ResponseError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type ORChatResponse struct {
	Choices []Choice       `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
	Error   *ResponseError `json:"error,omitempty"`
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

func NewORClient(apiKey string, modelIDs []string, toolRegistry *types.Registry, opts ...Option) *ORClient {
	if len(modelIDs) == 0 {
		modelIDs = []string{"z-ai/glm-5.3-flash"}
	}
	c := &ORClient{
		apiKey:  apiKey,
		models:  modelIDs,
		baseURL: defaultBaseURL,
		hc:      &http.Client{Timeout: 3 * time.Minute},
	}
	if toolRegistry != nil {
		c.Tools = toolRegistry.List()
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
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<(attempt-1)) * time.Second):
			case <-ctx.Done():
				return ORChatResponse{}, ctx.Err()
			}
		}
		resp, err := c.query(ctx, c.models[0], messages)
		if err == nil {
			if resp.Error != nil {
				lastErr = fmt.Errorf("openrouter: %s (code %d)", resp.Error.Message, resp.Error.Code)
				continue
			}
			if len(resp.Choices) == 0 {
				lastErr = fmt.Errorf("openrouter: response has no choices")
				continue
			}
			return resp, nil
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.Retryable() {
			return ORChatResponse{}, err
		}
		lastErr = err
	}
	return ORChatResponse{}, fmt.Errorf("openrouter: giving up after %d attempts: %w", maxAttempts, lastErr)
}

func (c *ORClient) query(ctx context.Context, model string, messages []types.Message) (ORChatResponse, error) {
	var chatResp ORChatResponse
	payload, err := json.Marshal(ORChatRequest{
		Model: model, Messages: messages, MaxTokens: c.maxTokens, Tools: c.toolDefinitions(),
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
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return chatResp, &APIError{Status: response.StatusCode, Body: string(body)}
	}
	if err := json.NewDecoder(response.Body).Decode(&chatResp); err != nil {
		return chatResp, err
	}
	return chatResp, nil

}
