package gemini

import (
	"testing"

	"ds2api/internal/promptcompat"
)

func TestNormalizeGeminiRequestNoThinkingModelForcesThinkingOff(t *testing.T) {
	req := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": "hello"}},
			},
		},
		"reasoning_effort": "high",
	}
	out, err := normalizeGeminiRequest(testGeminiConfig{}, "gemini-2.5-pro-nothinking", req, false)
	if err != nil {
		t.Fatalf("normalizeGeminiRequest error: %v", err)
	}
	if out.ResolvedModel != "deepseek-v4-pro-nothinking" {
		t.Fatalf("resolved model mismatch: got=%q", out.ResolvedModel)
	}
	if out.Thinking {
		t.Fatalf("expected nothinking model to force thinking off")
	}
	if out.Search {
		t.Fatalf("expected search=false, got=%v", out.Search)
	}
}

func TestNormalizeGeminiRequestParsesToolConfigModeAny(t *testing.T) {
	req := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": "hello"}},
			},
		},
		"tools": []any{
			map[string]any{
				"functionDeclarations": []any{
					map[string]any{"name": "search", "description": "Search", "parameters": map[string]any{"type": "object"}},
				},
			},
		},
		"toolConfig": map[string]any{
			"functionCallingConfig": map[string]any{
				"mode": "ANY",
			},
		},
	}
	out, err := normalizeGeminiRequest(testGeminiConfig{}, "gemini-2.5-pro", req, false)
	if err != nil {
		t.Fatalf("normalizeGeminiRequest error: %v", err)
	}
	if out.ToolChoice.Mode != promptcompat.ToolChoiceRequired {
		t.Fatalf("expected required tool choice, got %#v", out.ToolChoice)
	}
}
