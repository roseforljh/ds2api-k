package promptcompat

import (
	"strings"
	"testing"
)

func testToolRaw(name string) []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": "Run " + name,
				"parameters": map[string]any{
					"type": "object",
				},
			},
		},
	}
}

func TestBuildOpenAIToolPromptReturnsFallbackFormatWithoutTools(t *testing.T) {
	prompt, names := BuildOpenAIToolPrompt(nil, DefaultToolChoicePolicy())
	if !strings.Contains(prompt, "TOOL CALL FORMAT") {
		t.Fatalf("expected fallback format prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "No tool schemas were provided") {
		t.Fatalf("expected no-tools warning, got %q", prompt)
	}
	if names != nil {
		t.Fatalf("expected nil names, got %#v", names)
	}
}

func TestBuildOpenAIToolPromptBuildsToolInstructions(t *testing.T) {
	prompt, names := BuildOpenAIToolPrompt(testToolRaw("search"), DefaultToolChoicePolicy())
	if len(names) != 1 || names[0] != "search" {
		t.Fatalf("unexpected names: %#v", names)
	}
	for _, want := range []string{
		"You have access to these tools:",
		"Tool: search",
		"Description: Run search",
		"TOOL CALL FORMAT",
		"Remember: The ONLY valid way to use tools",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestBuildOpenAIToolPromptAddsRequiredAndOptionalParameterSummary(t *testing.T) {
	tools := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "Read",
				"description": "Read a file",
				"parameters": map[string]any{
					"type":     "object",
					"required": []any{"file_path"},
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string", "description": "Absolute path"},
						"offset":    map[string]any{"type": "integer"},
						"limit":     map[string]any{"type": "integer"},
					},
				},
			},
		},
	}

	prompt, _ := BuildOpenAIToolPrompt(tools, DefaultToolChoicePolicy())
	for _, want := range []string{
		"Parameter summary for Read:",
		"Required parameters:",
		"- file_path",
		"Optional parameters:",
		"- limit",
		"- offset",
		"Never call Read without: file_path.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestBuildOpenAIToolPromptRespectsToolChoiceNone(t *testing.T) {
	prompt, names := BuildOpenAIToolPrompt(testToolRaw("search"), ToolChoicePolicy{Mode: ToolChoiceNone})
	if !strings.Contains(prompt, "TOOL CALL FORMAT") {
		t.Fatalf("expected fallback format prompt even for tool_choice none, got %q", prompt)
	}
	if names != nil {
		t.Fatalf("expected nil names, got %#v", names)
	}
}

func TestBuildOpenAIToolPromptRequiredAddsRequiredInstruction(t *testing.T) {
	prompt, _ := BuildOpenAIToolPrompt(testToolRaw("search"), ToolChoicePolicy{Mode: ToolChoiceRequired})
	if !strings.Contains(prompt, "MUST call at least one tool") {
		t.Fatalf("expected required-tool instruction, got %q", prompt)
	}
}

func TestBuildOpenAIToolPromptForcedAddsForcedInstruction(t *testing.T) {
	prompt, names := BuildOpenAIToolPrompt(testToolRaw("search"), ToolChoicePolicy{
		Mode:       ToolChoiceForced,
		ForcedName: "search",
	})
	if len(names) != 1 || names[0] != "search" {
		t.Fatalf("unexpected names: %#v", names)
	}
	for _, want := range []string{
		"MUST call exactly this tool name: search",
		"Do not call any other tool.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected forced-tool instruction %q, got %q", want, prompt)
		}
	}
}
