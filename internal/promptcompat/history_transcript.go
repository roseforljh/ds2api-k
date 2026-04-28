package promptcompat

import (
	"strings"

	"ds2api/internal/prompt"
)

func BuildOpenAIHistoryTranscript(messages []any) string {
	return buildOpenAIFileTranscript(messages)
}

func BuildOpenAICurrentUserInputTranscript(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return BuildOpenAICurrentInputContextTranscript([]any{
		map[string]any{"role": "user", "content": text},
	})
}

func BuildOpenAICurrentInputContextTranscript(messages []any) string {
	return buildOpenAIFileTranscript(messages)
}

func BuildOpenAIToolPromptFileTranscript(toolPrompt string) string {
	text := strings.TrimSpace(toolPrompt)
	if text == "" {
		return ""
	}
	return buildOpenAIFileTranscript([]any{
		map[string]any{"role": "system", "content": text},
	})
}

func buildOpenAIFileTranscript(messages []any) string {
	normalized := NormalizeOpenAIMessagesForPrompt(messages, "")
	transcript := strings.TrimSpace(prompt.MessagesPrepare(normalized))
	if transcript == "" {
		return ""
	}
	return transcript
}
