package llm

import (
	"context"
	"stubbs/src/types"
)

type ModelClient interface {
	CompleteText(ctx context.Context, messages []types.Message) (ORChatResponse, error)
}
