package claude

import (
	"fmt"
	"strings"

	"ds2api/internal/config"
	"ds2api/internal/prompt"
	"ds2api/internal/promptcompat"
	"ds2api/internal/util"
)

type claudeNormalizedRequest struct {
	Standard           promptcompat.StandardRequest
	NormalizedMessages []any
}

func normalizeClaudeRequest(store ConfigReader, req map[string]any) (claudeNormalizedRequest, error) {
	model, _ := req["model"].(string)
	messagesRaw, _ := req["messages"].([]any)
	if strings.TrimSpace(model) == "" || len(messagesRaw) == 0 {
		return claudeNormalizedRequest{}, fmt.Errorf("request must include 'model' and 'messages'")
	}
	if _, ok := req["max_tokens"]; !ok {
		req["max_tokens"] = 8192
	}
	normalizedMessages := normalizeClaudeMessages(messagesRaw)
	payload := cloneMap(req)
	payload["messages"] = normalizedMessages
	toolsRequested, _ := req["tools"].([]any)
	payload["messages"] = injectClaudeToolPrompt(payload, normalizedMessages, toolsRequested)

	toolPolicy := parseClaudeToolChoice(req["tool_choice"], toolsRequested)

	dsPayload := convertClaudeToDeepSeek(payload, store)
	dsModel, _ := dsPayload["model"].(string)
	_, searchEnabled, ok := config.GetModelConfig(dsModel)
	if !ok {
		searchEnabled = false
	}
	thinkingEnabled := util.ResolveThinkingEnabled(req, false)
	if config.IsNoThinkingModel(dsModel) {
		thinkingEnabled = false
	}
	finalPrompt := prompt.MessagesPrepareWithThinking(toMessageMaps(dsPayload["messages"]), thinkingEnabled)
	toolNames := extractClaudeToolNames(toolsRequested)
	if len(toolNames) == 0 && len(toolsRequested) > 0 {
		toolNames = []string{"__any_tool__"}
	}

	return claudeNormalizedRequest{
		Standard: promptcompat.StandardRequest{
			Surface:        "anthropic_messages",
			RequestedModel: strings.TrimSpace(model),
			ResolvedModel:  dsModel,
			ResponseModel:  strings.TrimSpace(model),
			Messages:       payload["messages"].([]any),
			ToolsRaw:       toolsRequested,
			FinalPrompt:    finalPrompt,
			ToolNames:      toolNames,
			ToolChoice:     toolPolicy,
			Stream:         util.ToBool(req["stream"]),
			Thinking:       thinkingEnabled,
			Search:         searchEnabled,
		},
		NormalizedMessages: normalizedMessages,
	}, nil
}

func parseClaudeToolChoice(raw any, tools []any) promptcompat.ToolChoicePolicy {
	policy := promptcompat.DefaultToolChoicePolicy()
	if len(tools) > 0 {
		policy.Allowed = claudeToolNamesToSet(extractClaudeToolNames(tools))
	}
	if raw == nil {
		return policy
	}
	switch v := raw.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "any":
			policy.Mode = promptcompat.ToolChoiceRequired
		case "auto", "":
			policy.Mode = promptcompat.ToolChoiceAuto
		default:
			name := strings.TrimSpace(v)
			if name != "" && len(tools) > 0 {
				policy.Mode = promptcompat.ToolChoiceForced
				policy.ForcedName = name
				policy.Allowed = map[string]struct{}{name: {}}
			}
		}
	case map[string]any:
		typ := strings.ToLower(strings.TrimSpace(asClaudeString(v["type"])))
		switch typ {
		case "any":
			policy.Mode = promptcompat.ToolChoiceRequired
		case "tool":
			name := strings.TrimSpace(asClaudeString(v["name"]))
			if name == "" {
				policy.Mode = promptcompat.ToolChoiceRequired
			} else {
				policy.Mode = promptcompat.ToolChoiceForced
				policy.ForcedName = name
				policy.Allowed = map[string]struct{}{name: {}}
			}
		default:
			policy.Mode = promptcompat.ToolChoiceAuto
		}
	}
	return policy
}

func asClaudeString(v any) string {
	s, _ := v.(string)
	return s
}

func claudeToolNamesToSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func injectClaudeToolPrompt(payload map[string]any, normalizedMessages []any, tools []any) []any {
	if len(tools) == 0 {
		return normalizedMessages
	}
	toolPrompt := strings.TrimSpace(buildClaudeToolPrompt(tools))
	if toolPrompt == "" {
		return normalizedMessages
	}

	// Prefer top-level Anthropic-style system prompt when available.
	if systemText, ok := payload["system"].(string); ok && strings.TrimSpace(systemText) != "" {
		payload["system"] = mergeSystemPrompt(systemText, toolPrompt)
		return normalizedMessages
	}

	messages := cloneAnySlice(normalizedMessages)
	for i := range messages {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if !strings.EqualFold(strings.TrimSpace(role), "system") {
			continue
		}
		copied := cloneMap(msg)
		copied["content"] = mergeSystemPrompt(strings.TrimSpace(fmt.Sprintf("%v", copied["content"])), toolPrompt)
		messages[i] = copied
		return messages
	}

	return append([]any{map[string]any{"role": "system", "content": toolPrompt}}, messages...)
}

func mergeSystemPrompt(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n" + extra
	}
}

func cloneAnySlice(in []any) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}
