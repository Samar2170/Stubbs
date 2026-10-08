package stubbs_context

import (
	"strings"
	"stubbs/src/types"
	"unicode/utf8"
)

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	words := strings.Fields(s)
	chars := utf8.RuneCountInString(s)
	return max(len(words), (chars+3)/4)
}

func EstimateTokens(messages []types.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateTokens(msg.Content)
	}
	return total
}

func truncateToolOutput(s string, maxBytes int) string {
	s = strings.TrimSpace(s)

	if len(s) <= maxBytes {
		return s
	}

	half := maxBytes / 2

	head := s[:half]
	tail := s[len(s)-half:]

	return head +
		"\n\n... [tool output truncated] ...\n\n" +
		tail
}

func ContextUsage(messages []types.Message, modelLimit int) float64 {
	if modelLimit <= 0 {
		return 0
	}

	return float64(EstimateTokens(messages)) / float64(modelLimit)
}

func ShouldCompact(messages []types.Message, modelLimit int, ratio float64) bool {
	if modelLimit <= 0 {
		return false
	}

	if ratio <= 0 {
		ratio = 0.70
	}

	return ContextUsage(messages, modelLimit) >= ratio
}

func CleanWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
