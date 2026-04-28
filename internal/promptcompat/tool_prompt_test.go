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

func TestBuildOpenAIToolPromptReturnsEmptyWithoutTools(t *testing.T) {
	prompt, names := BuildOpenAIToolPrompt(nil, DefaultToolChoicePolicy())
	if prompt != "" {
		t.Fatalf("expected empty prompt, got %q", prompt)
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

func TestBuildOpenAIToolPromptRespectsToolChoiceNone(t *testing.T) {
	prompt, names := BuildOpenAIToolPrompt(testToolRaw("search"), ToolChoicePolicy{Mode: ToolChoiceNone})
	if prompt != "" {
		t.Fatalf("expected empty prompt, got %q", prompt)
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
