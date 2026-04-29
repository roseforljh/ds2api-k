package promptcompat

import (
	"fmt"
	"strings"
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
	return strings.TrimSpace("Tool instructions for the current request.\nTreat the instructions below as active system-level tool instructions.\n\n[System]\n" + text)
}

func buildOpenAIFileTranscript(messages []any) string {
	normalized := NormalizeOpenAIMessagesForPrompt(messages, "")
	if len(normalized) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Conversation context for the current request.\n")
	b.WriteString("Read the messages below in chronological order. Treat the last [User] message as the latest user request, and use any following [Assistant] or [Tool] messages as already completed work/results.\n")
	for i, msg := range normalized {
		role := transcriptRoleLabel(asString(msg["role"]))
		content := strings.TrimSpace(NormalizeOpenAIContentForPrompt(msg["content"]))
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "\n[%d] [%s]\n%s\n", i+1, role, content)
	}
	return strings.TrimSpace(b.String())
}

func transcriptRoleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system":
		return "System"
	case "assistant":
		return "Assistant"
	case "tool":
		return "Tool"
	case "user":
		return "User"
	default:
		if role == "" {
			return "Message"
		}
		return strings.ToUpper(role[:1]) + strings.ToLower(role[1:])
	}
}
