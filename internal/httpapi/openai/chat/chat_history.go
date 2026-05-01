package chat

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/chathistory"
	"ds2api/internal/config"
	openaifmt "ds2api/internal/format/openai"
	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/prompt"
	"ds2api/internal/promptcompat"
)

const adminWebUISourceHeader = "X-Ds2-Source"
const adminWebUISourceValue = "admin-webui-api-tester"

type chatHistorySession struct {
	store       *chathistory.Store
	entryID     string
	startedAt   time.Time
	lastPersist time.Time
	finalPrompt string
	startParams chathistory.StartParams
	disabled    bool
}

type accountInflightLimitReader interface {
	RuntimeAccountMaxInflight() int
}

func chatHistoryRetentionLimit(store shared.ConfigReader) int {
	if s, ok := store.(accountInflightLimitReader); ok {
		if n := s.RuntimeAccountMaxInflight(); n > 0 {
			return n
		}
	}
	return 2
}

func (h *Handler) configureChatHistoryRetention() {
	if h == nil || h.ChatHistory == nil {
		return
	}
	_, _ = h.ChatHistory.SetLimit(chatHistoryRetentionLimit(h.Store))
}

func startChatHistory(store *chathistory.Store, r *http.Request, a *auth.RequestAuth, stdReq promptcompat.StandardRequest) *chatHistorySession {
	if store == nil || r == nil || a == nil {
		return nil
	}
	if !store.Enabled() {
		return nil
	}
	if !shouldCaptureChatHistory(r) {
		return nil
	}
	historyMessages := chatHistoryMessages(stdReq)
	finalPrompt := chatHistoryFinalPrompt(stdReq)
	entry, err := store.Start(chathistory.StartParams{
		CallerID:       strings.TrimSpace(a.CallerID),
		AccountID:      strings.TrimSpace(a.AccountID),
		Model:          strings.TrimSpace(stdReq.ResponseModel),
		Stream:         stdReq.Stream,
		UserInput:      extractSingleUserInput(historyMessages),
		Messages:       extractAllMessages(historyMessages),
		HistoryText:    stdReq.HistoryText,
		ToolPromptText: "",
		FinalPrompt:    finalPrompt,
	})
	startParams := chathistory.StartParams{
		CallerID:       strings.TrimSpace(a.CallerID),
		AccountID:      strings.TrimSpace(a.AccountID),
		Model:          strings.TrimSpace(stdReq.ResponseModel),
		Stream:         stdReq.Stream,
		UserInput:      extractSingleUserInput(historyMessages),
		Messages:       extractAllMessages(historyMessages),
		HistoryText:    stdReq.HistoryText,
		ToolPromptText: "",
		FinalPrompt:    finalPrompt,
	}
	session := &chatHistorySession{
		store:       store,
		entryID:     entry.ID,
		startedAt:   time.Now(),
		lastPersist: time.Now(),
		finalPrompt: finalPrompt,
		startParams: startParams,
	}
	if err != nil {
		if entry.ID == "" {
			config.Logger.Warn("[chat_history] start failed", "error", err)
			return nil
		}
		config.Logger.Warn("[chat_history] start persisted in memory after write failure", "error", err)
	}
	return session
}

func chatHistoryMessages(stdReq promptcompat.StandardRequest) []any {
	if stdReq.CurrentInputFileApplied && len(stdReq.OriginalMessages) > 0 {
		return stdReq.OriginalMessages
	}
	return stdReq.Messages
}

func chatHistoryFinalPrompt(stdReq promptcompat.StandardRequest) string {
	return stripInlineToolInstructionsForHistory(stdReq.FinalPrompt)
}

func stripInlineToolInstructionsForHistory(text string) string {
	const startMarker = "=== TOOL INSTRUCTIONS, MUST FOLLOW ==="
	const endMarker = "=== END TOOL INSTRUCTIONS ==="
	start := strings.Index(text, startMarker)
	if start < 0 {
		return text
	}
	end := strings.Index(text[start:], endMarker)
	if end < 0 {
		return text
	}
	after := start + end + len(endMarker)
	for after < len(text) {
		switch text[after] {
		case '\r', '\n', ' ', '\t':
			after++
		default:
			stripped := text[:start] + text[after:]
			stripped = strings.ReplaceAll(stripped, "tool instructions, ", "")
			stripped = strings.ReplaceAll(stripped, ", tool instructions", "")
			return stripped
		}
	}
	return strings.TrimSpace(text[:start])
}

func shouldCaptureChatHistory(r *http.Request) bool {
	return r != nil
}

func extractSingleUserInput(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(asString(msg["role"])))
		if role != "user" {
			continue
		}
		if normalized := strings.TrimSpace(prompt.NormalizeContent(msg["content"])); normalized != "" {
			return normalized
		}
	}
	return ""
}

func extractAllMessages(messages []any) []chathistory.Message {
	out := make([]chathistory.Message, 0, len(messages))
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(asString(msg["role"])))
		content := strings.TrimSpace(prompt.NormalizeContent(msg["content"]))
		if role == "" || content == "" {
			continue
		}
		out = append(out, chathistory.Message{
			Role:    role,
			Content: content,
		})
	}
	return out
}

func (s *chatHistorySession) progress(thinking, content string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	now := time.Now()
	if now.Sub(s.lastPersist) < 250*time.Millisecond {
		return
	}
	s.lastPersist = now
	s.persistUpdate(chathistory.UpdateParams{
		Status:           "streaming",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       http.StatusOK,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
	})
}

func (s *chatHistorySession) success(statusCode int, thinking, content, finishReason string, usage map[string]any) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	s.persistUpdate(chathistory.UpdateParams{
		Status:           "success",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       statusCode,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Usage:            usage,
		Completed:        true,
	})
}

func (s *chatHistorySession) error(statusCode int, message, finishReason, thinking, content string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	s.persistUpdate(chathistory.UpdateParams{
		Status:           "error",
		ReasoningContent: thinking,
		Content:          content,
		Error:            message,
		StatusCode:       statusCode,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Completed:        true,
	})
}

func (s *chatHistorySession) stopped(thinking, content, finishReason string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	s.persistUpdate(chathistory.UpdateParams{
		Status:           "stopped",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       http.StatusOK,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Usage:            openaifmt.BuildChatUsage(s.finalPrompt, thinking, content),
		Completed:        true,
	})
}

func (s *chatHistorySession) retryMissingEntry() bool {
	if s == nil || s.store == nil || s.disabled {
		return false
	}
	entry, err := s.store.Start(s.startParams)
	if errors.Is(err, chathistory.ErrDisabled) {
		s.disabled = true
		return false
	}
	if entry.ID == "" {
		if err != nil {
			config.Logger.Warn("[chat_history] recreate missing entry failed", "error", err)
		}
		return false
	}
	s.entryID = entry.ID
	if err != nil {
		config.Logger.Warn("[chat_history] recreate missing entry persisted in memory after write failure", "error", err)
	}
	return true
}

func (s *chatHistorySession) persistUpdate(params chathistory.UpdateParams) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	if _, err := s.store.Update(s.entryID, params); err != nil {
		s.handlePersistError(params, err)
	}
}

func (s *chatHistorySession) handlePersistError(params chathistory.UpdateParams, err error) {
	if err == nil || s == nil {
		return
	}
	if errors.Is(err, chathistory.ErrDisabled) {
		s.disabled = true
		return
	}
	if isChatHistoryMissingError(err) {
		if s.retryMissingEntry() {
			if _, retryErr := s.store.Update(s.entryID, params); retryErr != nil {
				if errors.Is(retryErr, chathistory.ErrDisabled) || isChatHistoryMissingError(retryErr) {
					s.disabled = true
					return
				}
				config.Logger.Warn("[chat_history] retry after missing entry failed", "error", retryErr)
			}
			return
		}
		s.disabled = true
		return
	}
	config.Logger.Warn("[chat_history] update failed", "error", err)
}

func isChatHistoryMissingError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}
