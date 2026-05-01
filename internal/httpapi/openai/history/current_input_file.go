package history

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/promptcompat"
)

const (
	currentInputFilename    = "上下文.txt"
	currentInputContentType = "text/plain; charset=utf-8"
	currentInputPurpose     = "assistants"
)

func (s Service) ApplyCurrentInputFile(ctx context.Context, a *auth.RequestAuth, stdReq promptcompat.StandardRequest) (promptcompat.StandardRequest, error) {
	if s.DS == nil || s.Store == nil || a == nil || !s.Store.CurrentInputFileEnabled() {
		return stdReq, nil
	}
	if !hasPriorAssistantOrToolMessage(stdReq.Messages) {
		return stdReq, nil
	}
	threshold := s.Store.CurrentInputFileMinChars()

	index, text := latestUserInputForFile(stdReq.Messages)
	if index < 0 {
		return stdReq, nil
	}
	if len([]rune(text)) < threshold {
		return stdReq, nil
	}
	currentRequestRefFileIDs := currentInputRefFileIDs(stdReq.Messages, index)
	fileText := promptcompat.BuildOpenAICurrentInputContextTranscript(stdReq.Messages)
	if strings.TrimSpace(fileText) == "" {
		return stdReq, errors.New("current user input file produced empty transcript")
	}
	stdReq.HistoryText = fileText

	result, err := s.DS.UploadFile(ctx, a, dsclient.UploadFileRequest{
		Filename:    currentInputFilename,
		ContentType: currentInputContentType,
		Purpose:     currentInputPurpose,
		Data:        withUTF8BOM(fileText),
	}, 3)
	if err != nil {
		return stdReq, fmt.Errorf("upload current user input file: %w", err)
	}
	fileID := strings.TrimSpace(result.ID)
	if fileID == "" {
		return stdReq, errors.New("upload current user input file returned empty file id")
	}

	toolNames := stdReq.ToolNames
	toolsRawForPrompt := stdReq.ToolsRaw
	toolPromptText := ""
	if s.Store.CurrentInputToolPromptFileEnabled() {
		toolText, names := promptcompat.BuildOpenAIToolPrompt(stdReq.ToolsRaw, stdReq.ToolChoice)
		if strings.TrimSpace(toolText) != "" {
			toolPromptText = strings.TrimSpace(toolText)
			stdReq.ToolPromptText = toolPromptText
			toolNames = names
			toolsRawForPrompt = nil
		}
	}

	promptText := currentInputFilePrompt()
	if toolPromptText != "" {
		promptText = currentInputFilePromptWithInlineTools(toolPromptText)
	}
	messages := []any{
		map[string]any{
			"role":    "user",
			"content": promptText,
		},
	}

	stdReq.Messages = messages
	stdReq.CurrentInputFileApplied = true
	stdReq.RefFileIDs = prependUniqueRefFileID(currentRequestRefFileIDs, fileID)
	finalPrompt, builtToolNames := promptcompat.BuildOpenAIPrompt(messages, toolsRawForPrompt, "", stdReq.ToolChoice, stdReq.Thinking)
	stdReq.FinalPrompt = finalPrompt
	stdReq.PromptTokenText = fileText + "\n" + finalPrompt
	if toolPromptText != "" {
		stdReq.ToolNames = toolNames
	} else {
		stdReq.ToolNames = builtToolNames
	}
	return stdReq, nil
}

func currentInputRefFileIDs(messages []any, index int) []string {
	if index < 0 || index >= len(messages) {
		return nil
	}
	return promptcompat.CollectOpenAIRefFileIDs(map[string]any{
		"messages": []any{messages[index]},
	})
}

func hasPriorAssistantOrToolMessage(messages []any) bool {
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(shared.AsString(msg["role"]))) {
		case "assistant", "tool", "function":
			return true
		}
	}
	return false
}

func latestUserInputForFile(messages []any) (int, string) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(shared.AsString(msg["role"])))
		if role != "user" {
			continue
		}
		text := promptcompat.NormalizeOpenAIContentForPrompt(msg["content"])
		if strings.TrimSpace(text) == "" {
			return -1, ""
		}
		return i, text
	}
	return -1, ""
}

func currentInputFilePrompt() string {
	return "最新上下文已经做成文件发你了，你可以开始工作了。优先遵循附带上下文中的 WORKING STATE；若它要求 continue_agent_tail，就从最新 assistant/tool 进度继续，不要回到原始用户请求重做。请只使用本次请求附带的上下文、ref_file_ids 和最新用户消息；不要使用账号记忆、最近聊天、其他会话或未列入 ref_file_ids 的文件。若上下文工作状态为 no_active_working，不要重复已完成回答。只有最新用户消息明确要求继续时才继续；否则直接回答最新用户消息。"
}

func currentInputFilePromptWithInlineTools(toolPrompt string) string {
	return strings.TrimSpace("=== TOOL INSTRUCTIONS, MUST FOLLOW ===\n" +
		strings.TrimSpace(toolPrompt) +
		"\n=== END TOOL INSTRUCTIONS ===\n" +
		"最新上下文已经做成文件发你了，你可以开始工作了。优先遵循附带上下文中的 WORKING STATE；若它要求 continue_agent_tail，就从最新 assistant/tool 进度继续，不要回到原始用户请求重做。请只使用本次请求附带的上下文、工具说明、ref_file_ids 和最新用户消息；不要使用账号记忆、最近聊天、其他会话或未列入 ref_file_ids 的文件。若上下文工作状态为 no_active_working，不要重复已完成回答。只有最新用户消息明确要求继续时才继续；否则直接回答最新用户消息。")
}

func withUTF8BOM(text string) []byte {
	data := []byte(text)
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data
	}
	out := make([]byte, 0, len(data)+3)
	out = append(out, 0xEF, 0xBB, 0xBF)
	out = append(out, data...)
	return out
}
