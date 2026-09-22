package llm

import (
	"context"
	"fmt"
	"net/http"
	"stubbs/src/types"
)

type ModelClient interface {
	CompleteText(ctx context.Context, messages []types.Message) (ORChatResponse, error)
}

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openrouter: status %d: %s", e.Status, e.Body)
}

func (e *APIError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}
