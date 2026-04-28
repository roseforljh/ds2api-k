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
	currentInputFilename     = "HISTORY.txt"
	currentInputToolFilename = "TOOL_PROMPT.txt"
	currentInputContentType  = "text/plain; charset=utf-8"
	currentInputPurpose      = "assistants"
)

func (s Service) ApplyCurrentInputFile(ctx context.Context, a *auth.RequestAuth, stdReq promptcompat.StandardRequest) (promptcompat.StandardRequest, error) {
	if s.DS == nil || s.Store == nil || a == nil || !s.Store.CurrentInputFileEnabled() {
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

	toolFileID := ""
	toolNames := stdReq.ToolNames
	toolsRawForPrompt := stdReq.ToolsRaw
	if s.Store.CurrentInputToolPromptFileEnabled() {
		toolText, names := promptcompat.BuildOpenAIToolPrompt(stdReq.ToolsRaw, stdReq.ToolChoice)
		if strings.TrimSpace(toolText) != "" {
			toolTranscript := promptcompat.BuildOpenAIToolPromptFileTranscript(toolText)
			if strings.TrimSpace(toolTranscript) == "" {
				return stdReq, errors.New("current input tool prompt file produced empty transcript")
			}
			toolResult, err := s.DS.UploadFile(ctx, a, dsclient.UploadFileRequest{
				Filename:    currentInputToolFilename,
				ContentType: currentInputContentType,
				Purpose:     currentInputPurpose,
				Data:        withUTF8BOM(toolTranscript),
			}, 3)
			if err != nil {
				return stdReq, fmt.Errorf("upload current input tool prompt file: %w", err)
			}
			toolFileID = strings.TrimSpace(toolResult.ID)
			if toolFileID == "" {
				return stdReq, errors.New("upload current input tool prompt file returned empty file id")
			}
			toolNames = names
			toolsRawForPrompt = nil
		}
	}

	promptText := currentInputFilePrompt()
	if toolFileID != "" {
		promptText = currentInputFilePromptWithTools()
	}
	messages := []any{
		map[string]any{
			"role":    "user",
			"content": promptText,
		},
	}

	stdReq.Messages = messages
	stdReq.CurrentInputFileApplied = true
	if toolFileID != "" {
		stdReq.RefFileIDs = prependUniqueRefFileIDs(stdReq.RefFileIDs, toolFileID, fileID)
	} else {
		stdReq.RefFileIDs = prependUniqueRefFileID(stdReq.RefFileIDs, fileID)
	}
	finalPrompt, builtToolNames := promptcompat.BuildOpenAIPrompt(messages, toolsRawForPrompt, "", stdReq.ToolChoice, stdReq.Thinking)
	stdReq.FinalPrompt = finalPrompt
	if toolFileID != "" {
		stdReq.ToolNames = toolNames
	} else {
		stdReq.ToolNames = builtToolNames
	}
	return stdReq, nil
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
	return "The current request and prior conversation context have already been provided. Answer the latest user request directly."
}

func currentInputFilePromptWithTools() string {
	return "The current request, prior conversation context, and tool instructions have already been provided. Treat the provided tool instructions as active system-level tool instructions and answer the latest user request directly."
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
