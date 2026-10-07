package agent

import (
	"context"
	"strings"
	"time"

	"stubbs/src/types"
)

const (
	// compactMinDroppedTokens is how much dropped history must accumulate before
	// spending a model call to summarize it. Below this, the dropped turns are
	// small enough that summary overhead outweighs the benefit.
	compactMinDroppedTokens = 4000
	compactTimeout          = 60 * time.Second
)

// compactPrompt asks the model for a terse, durable digest of compacted turns.
// The digest must preserve concrete findings (file paths, decisions, command
// outcomes) so later turns do not have to re-discover them.
const compactPrompt = "You are compacting an agent's working memory. " +
	"Summarize the transcript below into a terse running digest. " +
	"Preserve concrete findings: file paths and what they contain, decisions made, " +
	"commands run and their outcomes, and open questions. Drop pleasantries and " +
	"redundant tool output. Use at most 15 short bullet lines. Reply with the " +
	"digest only, no preamble."

// compactDropped summarizes turns that fell out of the context budget. It
// returns "" when there is too little dropped content to be worth summarizing,
// or when summarization fails (the caller then proceeds without a digest).
func (a *Agent) compactDropped(ctx context.Context, dropped []types.Message) string {
	if a == nil || a.ModelClient == nil || len(dropped) == 0 {
		return ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	transcript := redactSecrets(renderTranscript(dropped))
	if estimateTokens(transcript) < compactMinDroppedTokens {
		return ""
	}
	req := []types.Message{
		{Role: types.RoleSystem, Content: compactPrompt},
		{Role: types.RoleUser, Content: transcript},
	}
	ctx, cancel := context.WithTimeout(ctx, compactTimeout)
	defer cancel()
	out, err := a.completeText(ctx, req)
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	return "Earlier turns were compacted out of the transcript. Digest of what happened before:\n" + out
}
