package llm

import (
	"bufio"
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
	// streaming requests server-sent events so tokens arrive incrementally
	// instead of buffering a whole (possibly multi-minute) completion.
	streaming bool
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

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ORChatRequest struct {
	Model         string                 `json:"model"`
	Messages      []types.Message        `json:"messages"`
	MaxTokens     int                    `json:"max_tokens,omitempty"`
	Tools         []types.ToolDefinition `json:"tools,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	StreamOptions *streamOptions         `json:"stream_options,omitempty"`
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

// WithStreaming enables or disables server-sent-event streaming.
func WithStreaming(enabled bool) Option {
	return func(c *ORClient) { c.streaming = enabled }
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
		streaming: true,
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
	reqBody := ORChatRequest{
		Model: model, Messages: messages, MaxTokens: c.maxTokens, Tools: tools,
	}
	if c.streaming {
		reqBody.Stream = true
		reqBody.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return chatResp, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(payload))
	if err != nil {
		return chatResp, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if c.streaming {
		req.Header.Set("Accept", "text/event-stream")
	}
	response, err := c.hc.Do(req)
	if err != nil {
		return chatResp, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return chatResp, &APIError{Status: response.StatusCode, Body: string(body)}
	}
	// The provider may ignore stream:true and reply with a single JSON body;
	// branch on the actual content type so both paths work.
	if reqBody.Stream && strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		return decodeStream(response.Body)
	}
	if err := json.NewDecoder(response.Body).Decode(&chatResp); err != nil {
		return chatResp, err
	}
	return chatResp, nil
}

// streamChunk is one SSE data frame: deltas for the assistant message plus an
// optional usage block on the final frame.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string           `json:"content"`
			ToolCalls []streamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage         `json:"usage,omitempty"`
	Error *ResponseError `json:"error,omitempty"`
}

type streamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// decodeStream reassembles an OpenAI-compatible SSE chat completion into the
// same ORChatResponse shape the non-streaming path returns.
func decodeStream(r io.Reader) (ORChatResponse, error) {
	var (
		content   strings.Builder
		usage     *Usage
		finish    string
		toolOrder []int
		toolCalls = map[int]*types.ToolCall{}
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return ORChatResponse{}, &APIError{Status: 200, Body: chunk.Error.Message}
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			content.WriteString(ch.Delta.Content)
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			for _, tc := range ch.Delta.ToolCalls {
				cur, ok := toolCalls[tc.Index]
				if !ok {
					cur = &types.ToolCall{}
					toolCalls[tc.Index] = cur
					toolOrder = append(toolOrder, tc.Index)
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Type != "" {
					cur.Type = tc.Type
				}
				cur.Function.Name += tc.Function.Name
				cur.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return ORChatResponse{}, err
	}
	msg := ResponseMessage{Role: "assistant", Content: content.String()}
	for _, idx := range toolOrder {
		tc := *toolCalls[idx]
		if tc.Type == "" {
			tc.Type = "function"
		}
		msg.ToolCalls = append(msg.ToolCalls, tc)
	}
	return ORChatResponse{
		Choices: []Choice{{Message: msg, FinishReason: finish}},
		Usage:   usage,
	}, nil
}
