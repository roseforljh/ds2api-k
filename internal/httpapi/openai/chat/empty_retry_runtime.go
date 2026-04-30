package chat

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsprotocol "ds2api/internal/deepseek/protocol"
	openaifmt "ds2api/internal/format/openai"
	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/sse"
	streamengine "ds2api/internal/stream"
	"ds2api/internal/toolcall"
	"ds2api/internal/toolstream"
)

type chatNonStreamResult struct {
	thinking              string
	toolDetectionThinking string
	text                  string
	contentFilter         bool
	detectedCalls         int
	malformedToolFeedback string
	body                  map[string]any
	finishReason          string
	responseMessageID     int
}

func retryFeedbackForChatResult(attempt int, result chatNonStreamResult) shared.RetryFeedback {
	if strings.TrimSpace(result.malformedToolFeedback) != "" {
		return shared.RetryFeedback{
			Attempt: attempt,
			Kind:    "malformed_tool_call",
			Summary: "invalid tool-call structure or empty required parameter",
			Raw:     result.malformedToolFeedback,
		}
	}
	return shared.RetryFeedback{
		Attempt: attempt,
		Kind:    "empty_output",
		Summary: "empty output or no valid tool call",
		Raw:     result.text,
	}
}

func (h *Handler) handleNonStreamWithRetry(w http.ResponseWriter, ctx context.Context, a *auth.RequestAuth, resp *http.Response, payload map[string]any, pow, completionID, model, finalPrompt string, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, historySession *chatHistorySession) remoteSessionTerminalState {
	attempts := 0
	currentResp := resp
	usagePrompt := finalPrompt
	accumulatedThinking := ""
	accumulatedToolDetectionThinking := ""
	retryStartedAt := time.Now()
	retryHistory := []shared.RetryFeedback{}
	for {
		result, ok := h.collectChatNonStreamAttempt(w, currentResp, completionID, model, usagePrompt, thinkingEnabled, searchEnabled, toolNames, toolsRaw)
		if !ok {
			return remoteTerminalFailed
		}
		accumulatedThinking += sse.TrimContinuationOverlap(accumulatedThinking, result.thinking)
		accumulatedToolDetectionThinking += sse.TrimContinuationOverlap(accumulatedToolDetectionThinking, result.toolDetectionThinking)
		result.thinking = accumulatedThinking
		result.toolDetectionThinking = accumulatedToolDetectionThinking
		detected := detectAssistantToolCalls(result.text, result.thinking, result.toolDetectionThinking, toolNames)
		result.detectedCalls = len(detected.Calls)
		if shouldRetryMalformedToolCall(detected, result.text) {
			result.malformedToolFeedback = result.text
			result.text = ""
		}
		result.body = openaifmt.BuildChatCompletionWithToolCalls(completionID, model, usagePrompt, result.thinking, result.text, detected.Calls, toolsRaw)
		result.finishReason = chatFinishReason(result.body)
		if !shouldRetryChatNonStream(result, attempts, retryStartedAt, time.Now()) {
			return h.finishChatNonStreamResult(w, result, attempts, usagePrompt, historySession)
		}

		attempts++
		retryHistory = shared.PushRetryFeedbackWindow(retryHistory, retryFeedbackForChatResult(attempts, result))
		config.Logger.Info("[openai_empty_retry] attempting synthetic retry", "surface", "chat.completions", "stream", false, "retry_attempt", attempts, "parent_message_id", result.responseMessageID)
		retryPow, powErr := h.DS.GetPow(ctx, a, 3)
		if powErr != nil {
			config.Logger.Warn("[openai_empty_retry] retry PoW fetch failed, falling back to original PoW", "surface", "chat.completions", "stream", false, "retry_attempt", attempts, "error", powErr)
			retryPow = pow
		}
		retryPayload := clonePayloadForMalformedOrEmptyRetry(payload, result.responseMessageID, result.malformedToolFeedback, len(toolNames) > 0, retryHistory)
		nextResp, err := h.DS.CallCompletion(ctx, a, retryPayload, retryPow, 3)
		if err != nil {
			if historySession != nil {
				historySession.error(http.StatusInternalServerError, "Failed to get completion.", "error", result.thinking, result.text)
			}
			writeOpenAIError(w, http.StatusInternalServerError, "Failed to get completion.")
			config.Logger.Warn("[openai_empty_retry] retry request failed", "surface", "chat.completions", "stream", false, "retry_attempt", attempts, "error", err)
			return remoteTerminalFailed
		}
		usagePrompt = usagePromptWithEmptyOutputRetry(finalPrompt, attempts)
		currentResp = nextResp
	}
}

func (h *Handler) collectChatNonStreamAttempt(w http.ResponseWriter, resp *http.Response, completionID, model, usagePrompt string, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any) (chatNonStreamResult, bool) {
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		writeOpenAIError(w, resp.StatusCode, string(body))
		return chatNonStreamResult{}, false
	}
	result := sse.CollectStream(resp, thinkingEnabled, true)
	stripReferenceMarkers := h.compatStripReferenceMarkers()
	finalThinking := cleanVisibleOutput(result.Thinking, stripReferenceMarkers)
	finalToolDetectionThinking := cleanVisibleOutput(result.ToolDetectionThinking, stripReferenceMarkers)
	finalText := cleanVisibleOutput(result.Text, stripReferenceMarkers)
	if searchEnabled {
		finalText = replaceCitationMarkersWithLinks(finalText, result.CitationLinks)
	}
	detected := detectAssistantToolCalls(finalText, finalThinking, finalToolDetectionThinking, toolNames)
	respBody := openaifmt.BuildChatCompletionWithToolCalls(completionID, model, usagePrompt, finalThinking, finalText, detected.Calls, toolsRaw)
	return chatNonStreamResult{
		thinking:              finalThinking,
		toolDetectionThinking: finalToolDetectionThinking,
		text:                  finalText,
		contentFilter:         result.ContentFilter,
		detectedCalls:         len(detected.Calls),
		body:                  respBody,
		finishReason:          chatFinishReason(respBody),
		responseMessageID:     result.ResponseMessageID,
	}, true
}

func (h *Handler) finishChatNonStreamResult(w http.ResponseWriter, result chatNonStreamResult, attempts int, usagePrompt string, historySession *chatHistorySession) remoteSessionTerminalState {
	if result.detectedCalls == 0 && shouldWriteUpstreamEmptyOutputError(result.text) {
		status, message, code := upstreamEmptyOutputDetail(result.contentFilter, result.text, result.thinking)
		if historySession != nil {
			historySession.error(status, message, code, result.thinking, result.text)
		}
		writeUpstreamEmptyOutputError(w, result.text, result.thinking, result.contentFilter)
		config.Logger.Info("[openai_empty_retry] terminal empty output", "surface", "chat.completions", "stream", false, "retry_attempts", attempts, "success_source", "none", "content_filter", result.contentFilter)
		return remoteTerminalFailed
	}
	if historySession != nil {
		historySession.success(http.StatusOK, result.thinking, result.text, result.finishReason, openaifmt.BuildChatUsage(usagePrompt, result.thinking, result.text))
	}
	writeJSON(w, http.StatusOK, result.body)
	source := "first_attempt"
	if attempts > 0 {
		source = "synthetic_retry"
	}
	config.Logger.Info("[openai_empty_retry] completed", "surface", "chat.completions", "stream", false, "retry_attempts", attempts, "success_source", source)
	return remoteTerminalSuccess
}

func chatFinishReason(respBody map[string]any) string {
	if choices, ok := respBody["choices"].([]map[string]any); ok && len(choices) > 0 {
		if fr, _ := choices[0]["finish_reason"].(string); strings.TrimSpace(fr) != "" {
			return fr
		}
	}
	return "stop"
}

func shouldRetryChatNonStream(result chatNonStreamResult, attempts int, startedAt, now time.Time) bool {
	return emptyOutputRetryEnabled() &&
		emptyOutputRetryWithinWindow(startedAt, now) &&
		attempts < emptyOutputRetryMaxAttempts() &&
		!result.contentFilter &&
		result.detectedCalls == 0 &&
		(strings.TrimSpace(result.text) == "" || strings.TrimSpace(result.malformedToolFeedback) != "")
}

func shouldRetryMalformedToolCall(parsed toolcall.ToolCallParseResult, text string) bool {
	return parsed.RejectedInvalid && strings.TrimSpace(text) != ""
}

func clonePayloadForMalformedOrEmptyRetry(payload map[string]any, parentMessageID int, malformedToolFeedback string, toolsAvailable bool, retryHistory []shared.RetryFeedback) map[string]any {
	if strings.TrimSpace(malformedToolFeedback) == "" && !toolsAvailable {
		clone := clonePayloadForEmptyOutputRetry(payload, parentMessageID)
		if original, _ := clone["prompt"].(string); len(retryHistory) > 0 {
			clone["prompt"] = appendToolEmptyOutputRetrySuffix(original, retryHistory)
		}
		return clone
	}
	clone := make(map[string]any, len(payload))
	for k, v := range payload {
		clone[k] = v
	}
	original, _ := payload["prompt"].(string)
	if strings.TrimSpace(malformedToolFeedback) != "" {
		clone["prompt"] = appendMalformedToolCallRetrySuffix(original, malformedToolFeedback, retryHistory)
	} else {
		clone["prompt"] = appendToolEmptyOutputRetrySuffix(original, retryHistory)
	}
	if parentMessageID > 0 {
		clone["parent_message_id"] = parentMessageID
	}
	return clone
}

func appendMalformedToolCallRetrySuffix(prompt string, malformedToolFeedback string, retryHistory []shared.RetryFeedback) string {
	prompt = strings.TrimRight(prompt, "\r\n\t ")
	parts := []string{
		"Your previous reply included an invalid tool call and was not shown to the user.",
	}
	if historyText := shared.RenderRetryFeedbackWindow(retryHistory); strings.TrimSpace(historyText) != "" {
		parts = append(parts, historyText)
	}
	parts = append(parts,
		"Use the tool instructions in the current request, including the current request tool schema and parameter summary.",
		"Regenerate your reply using the exact required tool-call structure below.",
		"Tool-call skeleton:",
		"<|DSML|tool_calls>",
		"  <|DSML|invoke name=\"VALID_TOOL_NAME_FROM_CURRENT_TOOL_LIST\">",
		"    <|DSML|parameter name=\"VALID_PARAMETER_NAME\"><![CDATA[NON_EMPTY_VALUE]]></|DSML|parameter>",
		"  </|DSML|invoke>",
		"</|DSML|tool_calls>",
		"Rules:",
		"1) Do not copy placeholder names literally.",
		"2) The tool name must be one allowed tool name from the current request.",
		"3) Parameter names must come from that tool's schema in the current request.",
		"4) Required parameters must be present and non-empty.",
		"5) Output no explanation, no markdown fences, and no extra text.",
		"6) If you use a tool, your first non-whitespace characters must be exactly <|DSML|tool_calls>.",
		"Invalid previous reply:",
		strings.TrimSpace(malformedToolFeedback),
		"Now output only one corrected tool call and nothing else.",
	)
	suffix := strings.Join(parts, "\n")
	if prompt == "" {
		return suffix
	}
	return prompt + "\n\n" + suffix
}

func appendToolEmptyOutputRetrySuffix(prompt string, retryHistory []shared.RetryFeedback) string {
	prompt = strings.TrimRight(prompt, "\r\n\t ")
	parts := []string{
		"Your previous reply was invalid or empty and was not shown to the user.",
	}
	if historyText := shared.RenderRetryFeedbackWindow(retryHistory); strings.TrimSpace(historyText) != "" {
		parts = append(parts, historyText)
	}
	parts = append(parts,
		"Use the tool instructions in the current request, including the current request tool schema and parameter summary.",
		"The previous attempt ended without natural-language content or a valid tool call. Do not return an empty response.",
		"Regenerate your reply using exactly one of these forms:",
		"A) Normal answer: plain natural-language answer only.",
		"B) Tool call skeleton:",
		"<|DSML|tool_calls>",
		"  <|DSML|invoke name=\"VALID_TOOL_NAME_FROM_CURRENT_TOOL_LIST\">",
		"    <|DSML|parameter name=\"VALID_PARAMETER_NAME\"><![CDATA[NON_EMPTY_VALUE]]></|DSML|parameter>",
		"  </|DSML|invoke>",
		"</|DSML|tool_calls>",
		"Rules:",
		"1) Do not copy placeholder names literally.",
		"2) If using a tool, the tool name must be one allowed tool name from the current request.",
		"3) Parameter names must come from that tool's schema in the current request.",
		"4) Required parameters must be present and non-empty.",
		"5) Output no explanation, no markdown fences, and no extra text.",
		"6) If you use a tool, your first non-whitespace characters must be exactly <|DSML|tool_calls>.",
		"7) If no tool is needed, answer the user directly instead of returning empty output.",
		"Now output only the corrected final answer or one valid tool call.",
	)
	suffix := strings.Join(parts, "\n")
	if prompt == "" {
		return suffix
	}
	return prompt + "\n\n" + suffix
}

func (h *Handler) handleStreamWithRetry(w http.ResponseWriter, r *http.Request, a *auth.RequestAuth, resp *http.Response, payload map[string]any, pow, completionID, model, finalPrompt string, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, historySession *chatHistorySession) remoteSessionTerminalState {
	streamRuntime, initialType, ok := h.prepareChatStreamRuntime(w, resp, completionID, model, finalPrompt, thinkingEnabled, searchEnabled, toolNames, toolsRaw, historySession)
	if !ok {
		return remoteTerminalFailed
	}
	attempts := 0
	retryStartedAt := time.Now()
	retryHistory := []shared.RetryFeedback{}
	currentResp := resp
	for {
		withinWindow := emptyOutputRetryWithinWindow(retryStartedAt, time.Now())
		terminal, terminalWritten, retryable := h.consumeChatStreamAttempt(r, currentResp, streamRuntime, initialType, thinkingEnabled, historySession, withinWindow && attempts < emptyOutputRetryMaxAttempts())
		if terminalWritten {
			logChatStreamTerminal(streamRuntime, attempts)
			return terminal
		}
		if !retryable || !emptyOutputRetryEnabled() || !withinWindow || attempts >= emptyOutputRetryMaxAttempts() {
			streamRuntime.finalize("stop", false)
			recordChatStreamHistory(streamRuntime, historySession)
			config.Logger.Info("[openai_empty_retry] terminal empty output", "surface", "chat.completions", "stream", true, "retry_attempts", attempts, "success_source", "none")
			return remoteTerminalFailed
		}
		attempts++
		feedback := shared.RetryFeedback{Attempt: attempts, Kind: "empty_output", Summary: "empty output or no valid tool call", Raw: streamRuntime.text.String()}
		if strings.TrimSpace(streamRuntime.malformedToolFeedback) != "" {
			feedback.Kind = "malformed_tool_call"
			feedback.Summary = "invalid tool-call structure or empty required parameter"
			feedback.Raw = streamRuntime.malformedToolFeedback
		}
		retryHistory = shared.PushRetryFeedbackWindow(retryHistory, feedback)
		config.Logger.Info("[openai_empty_retry] attempting synthetic retry", "surface", "chat.completions", "stream", true, "retry_attempt", attempts, "parent_message_id", streamRuntime.responseMessageID)
		retryPow, powErr := h.DS.GetPow(r.Context(), a, 3)
		if powErr != nil {
			config.Logger.Warn("[openai_empty_retry] retry PoW fetch failed, falling back to original PoW", "surface", "chat.completions", "stream", true, "retry_attempt", attempts, "error", powErr)
			retryPow = pow
		}
		malformedFeedback := streamRuntime.malformedToolFeedback
		nextResp, err := h.DS.CallCompletion(r.Context(), a, clonePayloadForMalformedOrEmptyRetry(payload, streamRuntime.responseMessageID, malformedFeedback, len(toolNames) > 0, retryHistory), retryPow, 3)
		if err != nil {
			failChatStreamRetry(streamRuntime, historySession, http.StatusInternalServerError, "Failed to get completion.", "error")
			config.Logger.Warn("[openai_empty_retry] retry request failed", "surface", "chat.completions", "stream", true, "retry_attempt", attempts, "error", err)
			return remoteTerminalFailed
		}
		if nextResp.StatusCode != http.StatusOK {
			defer func() { _ = nextResp.Body.Close() }()
			body, _ := io.ReadAll(nextResp.Body)
			failChatStreamRetry(streamRuntime, historySession, nextResp.StatusCode, string(body), "error")
			return remoteTerminalFailed
		}
		if strings.TrimSpace(malformedFeedback) != "" {
			streamRuntime.text.Reset()
			streamRuntime.toolSieve = toolstream.State{}
			streamRuntime.malformedToolFeedback = ""
		}
		streamRuntime.finalPrompt = usagePromptWithEmptyOutputRetry(finalPrompt, attempts)
		currentResp = nextResp
	}
}

func (h *Handler) prepareChatStreamRuntime(w http.ResponseWriter, resp *http.Response, completionID, model, finalPrompt string, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, historySession *chatHistorySession) (*chatStreamRuntime, string, bool) {
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if historySession != nil {
			historySession.error(resp.StatusCode, string(body), "error", "", "")
		}
		writeOpenAIError(w, resp.StatusCode, string(body))
		return nil, "", false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	_, canFlush := w.(http.Flusher)
	if !canFlush {
		config.Logger.Warn("[stream] response writer does not support flush; streaming may be buffered")
	}
	initialType := "text"
	if thinkingEnabled {
		initialType = "thinking"
	}
	streamRuntime := newChatStreamRuntime(
		w, rc, canFlush, completionID, time.Now().Unix(), model, finalPrompt,
		thinkingEnabled, searchEnabled, h.compatStripReferenceMarkers(), toolNames, toolsRaw,
		len(toolNames) > 0, h.toolcallFeatureMatchEnabled() && h.toolcallEarlyEmitHighConfidence(),
	)
	return streamRuntime, initialType, true
}

func (h *Handler) consumeChatStreamAttempt(r *http.Request, resp *http.Response, streamRuntime *chatStreamRuntime, initialType string, thinkingEnabled bool, historySession *chatHistorySession, allowDeferEmpty bool) (remoteSessionTerminalState, bool, bool) {
	defer func() { _ = resp.Body.Close() }()
	finalReason := "stop"
	contextCancelled := false
	streamengine.ConsumeSSE(streamengine.ConsumeConfig{
		Context:             r.Context(),
		Body:                resp.Body,
		ThinkingEnabled:     thinkingEnabled,
		InitialType:         initialType,
		KeepAliveInterval:   time.Duration(dsprotocol.KeepAliveTimeout) * time.Second,
		IdleTimeout:         time.Duration(dsprotocol.StreamIdleTimeout) * time.Second,
		MaxKeepAliveNoInput: dsprotocol.MaxKeepaliveCount,
	}, streamengine.ConsumeHooks{
		OnKeepAlive: streamRuntime.sendKeepAlive,
		OnParsed: func(parsed sse.LineResult) streamengine.ParsedDecision {
			decision := streamRuntime.onParsed(parsed)
			if historySession != nil {
				historySession.progress(streamRuntime.thinking.String(), streamRuntime.text.String())
			}
			return decision
		},
		OnFinalize: func(reason streamengine.StopReason, _ error) {
			if string(reason) == "content_filter" {
				finalReason = "content_filter"
			}
		},
		OnContextDone: func() {
			contextCancelled = true
			if historySession != nil {
				historySession.stopped(streamRuntime.thinking.String(), streamRuntime.text.String(), string(streamengine.StopReasonContextCancelled))
			}
		},
	})
	if contextCancelled {
		return remoteTerminalStopped, true, false
	}
	terminalWritten := streamRuntime.finalize(finalReason, allowDeferEmpty && finalReason != "content_filter")
	if terminalWritten {
		recordChatStreamHistory(streamRuntime, historySession)
		if streamRuntime.finalErrorMessage != "" {
			return remoteTerminalFailed, true, false
		}
		return remoteTerminalSuccess, true, false
	}
	return remoteTerminalFailed, false, true
}

func recordChatStreamHistory(streamRuntime *chatStreamRuntime, historySession *chatHistorySession) {
	if historySession == nil {
		return
	}
	if streamRuntime.finalErrorMessage != "" {
		historySession.error(streamRuntime.finalErrorStatus, streamRuntime.finalErrorMessage, streamRuntime.finalErrorCode, streamRuntime.thinking.String(), streamRuntime.text.String())
		return
	}
	historySession.success(http.StatusOK, streamRuntime.finalThinking, streamRuntime.finalText, streamRuntime.finalFinishReason, streamRuntime.finalUsage)
}

func failChatStreamRetry(streamRuntime *chatStreamRuntime, historySession *chatHistorySession, status int, message, code string) {
	streamRuntime.sendFailedChunk(status, message, code)
	if historySession != nil {
		historySession.error(status, message, code, streamRuntime.thinking.String(), streamRuntime.text.String())
	}
}

func logChatStreamTerminal(streamRuntime *chatStreamRuntime, attempts int) {
	source := "first_attempt"
	if attempts > 0 {
		source = "synthetic_retry"
	}
	if streamRuntime.finalErrorMessage != "" {
		config.Logger.Info("[openai_empty_retry] terminal empty output", "surface", "chat.completions", "stream", true, "retry_attempts", attempts, "success_source", "none", "error_code", streamRuntime.finalErrorCode)
		return
	}
	config.Logger.Info("[openai_empty_retry] completed", "surface", "chat.completions", "stream", true, "retry_attempts", attempts, "success_source", source)
}
