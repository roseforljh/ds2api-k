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
	currentInputFilename    = "HISTORY.txt"
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
	stdReq.RefFileIDs = prependUniqueRefFileID(nil, fileID)
	finalPrompt, builtToolNames := promptcompat.BuildOpenAIPrompt(messages, toolsRawForPrompt, "", stdReq.ToolChoice, stdReq.Thinking)
	stdReq.FinalPrompt = finalPrompt
	if toolPromptText != "" {
		stdReq.ToolNames = toolNames
	} else {
		stdReq.ToolNames = builtToolNames
	}
	return stdReq, nil
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
	return "Read HISTORY.txt WORKING STATE first. If it says no_active_working, do not repeat completed answers. Use only this request's attached context, ref_file_ids, and latest user message; no account memories, recent chats, or other sessions. Continue only if the latest user asks; otherwise answer the latest user directly."
}

func currentInputFilePromptWithInlineTools(toolPrompt string) string {
	return strings.TrimSpace("=== TOOL INSTRUCTIONS, MUST FOLLOW ===\n" +
		strings.TrimSpace(toolPrompt) +
		"\n=== END TOOL INSTRUCTIONS ===\n" +
		"Read HISTORY.txt WORKING STATE. If it says no_active_working, do not repeat completed answers. Use only attached context, tool instructions, ref_file_ids, and latest user message; no account memories, recent chats, or other sessions.")
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
