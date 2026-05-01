package openai

import "ds2api/internal/util"

func BuildChatUsage(finalPrompt, finalThinking, finalText string) map[string]any {
	return BuildChatUsageForModel("", finalPrompt, finalThinking, finalText)
}

func BuildChatUsageForModel(model, finalPrompt, finalThinking, finalText string) map[string]any {
	promptTokens := util.CountPromptTokens(finalPrompt, model)
	reasoningTokens := util.CountOutputTokens(finalThinking, model)
	completionTokens := util.CountOutputTokens(finalText, model)
	return map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": reasoningTokens + completionTokens,
		"total_tokens":      promptTokens + reasoningTokens + completionTokens,
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": reasoningTokens,
		},
	}
}

func BuildResponsesUsage(finalPrompt, finalThinking, finalText string) map[string]any {
	return BuildResponsesUsageForModel("", finalPrompt, finalThinking, finalText)
}

func BuildResponsesUsageForModel(model, finalPrompt, finalThinking, finalText string) map[string]any {
	promptTokens := util.CountPromptTokens(finalPrompt, model)
	reasoningTokens := util.CountOutputTokens(finalThinking, model)
	completionTokens := util.CountOutputTokens(finalText, model)
	return map[string]any{
		"input_tokens":  promptTokens,
		"output_tokens": reasoningTokens + completionTokens,
		"total_tokens":  promptTokens + reasoningTokens + completionTokens,
	}
}
