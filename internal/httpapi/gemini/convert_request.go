package gemini

import (
	"fmt"
	"strings"

	"ds2api/internal/config"
	"ds2api/internal/promptcompat"
	"ds2api/internal/util"
)

//nolint:unused // kept for native Gemini adapter route compatibility.
func normalizeGeminiRequest(store ConfigReader, routeModel string, req map[string]any, stream bool) (promptcompat.StandardRequest, error) {
	requestedModel := strings.TrimSpace(routeModel)
	if requestedModel == "" {
		return promptcompat.StandardRequest{}, fmt.Errorf("model is required in request path")
	}

	resolvedModel, ok := config.ResolveModel(store, requestedModel)
	if !ok {
		return promptcompat.StandardRequest{}, fmt.Errorf("model %q is not available", requestedModel)
	}
	defaultThinkingEnabled, searchEnabled, _ := config.GetModelConfig(resolvedModel)
	thinkingEnabled := util.ResolveThinkingEnabled(req, defaultThinkingEnabled)
	if config.IsNoThinkingModel(resolvedModel) {
		thinkingEnabled = false
	}

	messagesRaw := geminiMessagesFromRequest(req)
	if len(messagesRaw) == 0 {
		return promptcompat.StandardRequest{}, fmt.Errorf("request must include non-empty contents")
	}

	toolsRaw := convertGeminiTools(req["tools"])
	toolPolicy := parseGeminiToolConfig(req["toolConfig"], toolsRaw)
	finalPrompt, toolNames := promptcompat.BuildOpenAIPrompt(messagesRaw, toolsRaw, "", toolPolicy, thinkingEnabled)
	passThrough := collectGeminiPassThrough(req)

	return promptcompat.StandardRequest{
		Surface:        "google_gemini",
		RequestedModel: requestedModel,
		ResolvedModel:  resolvedModel,
		ResponseModel:  requestedModel,
		Messages:       messagesRaw,
		ToolsRaw:       toolsRaw,
		FinalPrompt:    finalPrompt,
		ToolNames:      toolNames,
		ToolChoice:     toolPolicy,
		Stream:         stream,
		Thinking:       thinkingEnabled,
		Search:         searchEnabled,
		PassThrough:    passThrough,
	}, nil
}

func parseGeminiToolConfig(raw any, tools []any) promptcompat.ToolChoicePolicy {
	policy := promptcompat.DefaultToolChoicePolicy()
	if len(tools) > 0 {
		policy.Allowed = geminiToolNamesToSet(tools)
	}
	cfg, _ := raw.(map[string]any)
	if len(cfg) == 0 {
		return policy
	}
	fnCfg, _ := cfg["functionCallingConfig"].(map[string]any)
	if len(fnCfg) == 0 {
		return policy
	}
	mode := strings.ToLower(strings.TrimSpace(asString(fnCfg["mode"])))
	switch mode {
	case "none":
		policy.Mode = promptcompat.ToolChoiceNone
		policy.Allowed = nil
	case "any":
		policy.Mode = promptcompat.ToolChoiceRequired
	case "auto", "":
		policy.Mode = promptcompat.ToolChoiceAuto
	}
	return policy
}

func geminiToolNamesToSet(tools []any) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if len(fn) == 0 {
			fn = tool
		}
		name := strings.TrimSpace(asString(fn["name"]))
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
