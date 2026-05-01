package prompt

import (
	"strings"
	"testing"
)

func TestNormalizeContentNilReturnsEmpty(t *testing.T) {
	if got := NormalizeContent(nil); got != "" {
		t.Fatalf("expected empty string for nil content, got %q", got)
	}
}

func TestMessagesPrepareNilContentNoNullLiteral(t *testing.T) {
	messages := []map[string]any{
		{"role": "assistant", "content": nil},
		{"role": "user", "content": "ok"},
	}
	got := MessagesPrepare(messages)
	if got == "" {
		t.Fatalf("expected non-empty output")
	}
	if got == "null" {
		t.Fatalf("expected no null literal output, got %q", got)
	}
}

func TestMessagesPrepareUsesTurnSuffixes(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "System rule"},
		{"role": "user", "content": "Question"},
		{"role": "assistant", "content": "Answer"},
	}
	got := MessagesPrepare(messages)
	if !strings.HasPrefix(got, "<｜begin▁of▁sentence｜>") {
		t.Fatalf("expected begin-of-sentence marker, got %q", got)
	}
	if !strings.Contains(got, "<｜System｜>System rule<｜end▁of▁instructions｜>") {
		t.Fatalf("expected system instructions suffix, got %q", got)
	}
	if !strings.Contains(got, "<｜User｜>Question") {
		t.Fatalf("expected user question, got %q", got)
	}
	if !strings.Contains(got, "<｜Assistant｜>Answer<｜end▁of▁sentence｜>") {
		t.Fatalf("expected assistant sentence suffix, got %q", got)
	}
	if strings.Contains(got, "<think>") || strings.Contains(got, "</think>") {
		t.Fatalf("did not expect think tags in prompt, got %q", got)
	}
}

func TestNormalizeContentArrayFallsBackToContentWhenTextEmpty(t *testing.T) {
	got := NormalizeContent([]any{
		map[string]any{"type": "text", "text": "", "content": "from-content"},
	})
	if got != "from-content" {
		t.Fatalf("expected fallback to content when text is empty, got %q", got)
	}
}

func TestMessagesPrepareWithThinkingPreservesPromptShape(t *testing.T) {
	messages := []map[string]any{{"role": "user", "content": "Question"}}
	gotThinking := MessagesPrepareWithThinking(messages, true)
	gotPlain := MessagesPrepareWithThinking(messages, false)
	if gotThinking != gotPlain {
		t.Fatalf("expected thinking flag not to add extra continuity instructions, got thinking=%q plain=%q", gotThinking, gotPlain)
	}
	if !strings.HasSuffix(gotThinking, "<｜Assistant｜>") {
		t.Fatalf("expected assistant suffix, got %q", gotThinking)
	}
}

func TestMessagesPrepareInjectsOfficialDSMLToolsIntoSystemPrompt(t *testing.T) {
	messages := []map[string]any{
		{
			"role":    "system",
			"content": "You are helper",
			"tools": []any{
				map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        "get_current_datetime",
						"description": "Get current datetime",
						"parameters": map[string]any{
							"type":                 "object",
							"properties":           map[string]any{},
							"additionalProperties": false,
						},
					},
				},
			},
		},
		{"role": "user", "content": "现在几点"},
	}
	got := MessagesPrepareWithThinking(messages, true)
	if !strings.Contains(got, `You can invoke tools by writing a "<｜DSML｜tool_calls>" block`) {
		t.Fatalf("expected official DSML tool instructions, got %q", got)
	}
	if !strings.Contains(got, `<｜DSML｜parameter name="$PARAMETER_NAME" string="true|false">$PARAMETER_VALUE`) {
		t.Fatalf("expected official DSML parameter schema, got %q", got)
	}
	if !strings.Contains(got, `"name":"get_current_datetime"`) {
		t.Fatalf("expected tool schema injected into prompt, got %q", got)
	}
}

func TestMessagesPrepareMergesToolRoleIntoUserToolResultBlock(t *testing.T) {
	messages := []map[string]any{
		{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"type": "function",
					"id":   "call_1",
					"function": map[string]any{
						"name":      "get_current_datetime",
						"arguments": `{}`,
					},
				},
			},
		},
		{"role": "tool", "tool_call_id": "call_1", "content": `{"now":"2026-05-01T00:00:00Z"}`},
	}
	got := MessagesPrepareWithThinking(messages, true)
	if strings.Contains(got, "<｜Tool｜>") {
		t.Fatalf("expected no legacy tool role markers, got %q", got)
	}
	if !strings.Contains(got, `<tool_result>{"now":"2026-05-01T00:00:00Z"}</tool_result>`) {
		t.Fatalf("expected merged tool_result block, got %q", got)
	}
}

func TestMessagesPrepareEncodesAssistantToolCallArgumentsAsOfficialDSMLParameters(t *testing.T) {
	messages := []map[string]any{
		{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"type": "function",
					"id":   "call_1",
					"function": map[string]any{
						"name":      "Read",
						"arguments": `{"file_path":"README.md","limit":55,"recursive":false}`,
					},
				},
			},
		},
	}
	got := MessagesPrepareWithThinking(messages, true)
	for _, want := range []string{
		`<｜DSML｜tool_calls>`,
		`<｜DSML｜invoke name="Read">`,
		`<｜DSML｜parameter name="file_path" string="true">README.md</｜DSML｜parameter>`,
		`<｜DSML｜parameter name="limit" string="false">55</｜DSML｜parameter>`,
		`<｜DSML｜parameter name="recursive" string="false">false</｜DSML｜parameter>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected official DSML parameter encoding %q, got %q", want, got)
		}
	}
}
