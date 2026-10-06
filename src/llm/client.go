package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"stubbs/src/types"
	"sync"
	"time"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"
const maxAttempts = 3

type ORClient struct {
	apiKey    string
	mu        sync.RWMutex // guards models/sendTools; the active model can change at runtime
	models    []string
	maxTokens int
	baseURL   string
	hc        *http.Client
	Tools     []types.Tool
	// sendTools controls whether tool definitions are sent with requests. It is
	// cleared when the agent falls back to the text tool protocol.
	sendTools bool
}

// SetNativeTools enables or disables sending tool definitions. It is safe to
// call while a request is in flight.
func (c *ORClient) SetNativeTools(enabled bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.sendTools = enabled
	c.mu.Unlock()
}

// SetModel switches the active model. The change takes effect on the next
// request. Safe to call while CompleteText is in flight.
func (c *ORClient) SetModel(model string) {
	if c == nil || model == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.models) == 0 {
		c.models = []string{model}
		return
	}
	c.models[0] = model
}

// Model returns the active model.
func (c *ORClient) Model() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.models) == 0 {
		return ""
	}
	return c.models[0]
}

func (c *ORClient) activeModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.models) == 0 {
		return ""
	}
	return c.models[0]
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
		apiKey:    apiKey,
		models:    modelIDs,
		baseURL:   defaultBaseURL,
		hc:        &http.Client{Timeout: 3 * time.Minute},
		sendTools: true,
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
	c.mu.RLock()
	send := c.sendTools
	c.mu.RUnlock()
	var tools []types.ToolDefinition
	if send {
		tools = c.toolDefinitions()
	}
	return c.complete(ctx, messages, tools)
}

// Complete is like CompleteText but does not offer any tools. It is used for
// auxiliary calls (such as memory summarization) that must return plain text.
func (c *ORClient) Complete(ctx context.Context, messages []types.Message) (ORChatResponse, error) {
	return c.complete(ctx, messages, nil)
}

func (c *ORClient) complete(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (ORChatResponse, error) {
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
		resp, err := c.query(ctx, c.activeModel(), messages, tools)
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
		if !retryable(ctx, err) {
			return ORChatResponse{}, err
		}
		lastErr = err
	}
	return ORChatResponse{}, fmt.Errorf("openrouter: giving up after %d attempts: %w", maxAttempts, lastErr)
}

// retryable reports whether err is worth another attempt. API errors are
// retried when the status is transient; transport timeouts (including
// http.Client.Timeout) are retried too, since a slow provider response is not
// fatal. A cancelled or expired parent context is never retried: the caller
// owns that deadline and handles it explicitly.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (c *ORClient) query(ctx context.Context, model string, messages []types.Message, tools []types.ToolDefinition) (ORChatResponse, error) {
	var chatResp ORChatResponse
	payload, err := json.Marshal(ORChatRequest{
		Model: model, Messages: messages, MaxTokens: c.maxTokens, Tools: tools,
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
