package promptcompat

import (
	"strings"
	"testing"

	"ds2api/internal/util"
)

func TestNormalizeOpenAIMessagesForPrompt_AssistantToolCallsAndToolResult(t *testing.T) {
	raw := []any{
		map[string]any{"role": "system", "content": "You are helpful"},
		map[string]any{"role": "user", "content": "查北京天气"},
		map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{
				map[string]any{
					"id":   "call_1",
					"type": "function",
					"function": map[string]any{
						"name":      "get_weather",
						"arguments": "{\"city\":\"beijing\"}",
					},
				},
			},
		},
		map[string]any{
			"role":         "tool",
			"tool_call_id": "call_1",
			"name":         "get_weather",
			"content":      "{\"temp\":18}",
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 4 {
		t.Fatalf("expected 4 normalized messages with assistant tool history preserved, got %d", len(normalized))
	}
	assistantContent, _ := normalized[2]["content"].(string)
	if !strings.Contains(assistantContent, "<|DSML|tool_calls>") {
		t.Fatalf("assistant tool history should be preserved in DSML form, got %q", assistantContent)
	}
	if !strings.Contains(assistantContent, `<|DSML|invoke name="get_weather">`) {
		t.Fatalf("expected tool name in preserved history, got %q", assistantContent)
	}
	if !strings.Contains(normalized[3]["content"].(string), `"temp":18`) {
		t.Fatalf("tool result should be transparently forwarded, got %#v", normalized[3]["content"])
	}

	prompt := util.MessagesPrepare(normalized)
	if !strings.Contains(prompt, "<|DSML|tool_calls>") {
		t.Fatalf("expected preserved assistant tool history in prompt: %q", prompt)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_ToolObjectContentPreserved(t *testing.T) {
	raw := []any{
		map[string]any{
			"role":         "tool",
			"tool_call_id": "call_2",
			"name":         "get_weather",
			"content": map[string]any{
				"temp":      18,
				"condition": "sunny",
			},
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	got, _ := normalized[0]["content"].(string)
	if !strings.Contains(got, `"temp":18`) || !strings.Contains(got, `"condition":"sunny"`) {
		t.Fatalf("expected serialized object in tool content, got %q", got)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_ToolArrayBlocksJoined(t *testing.T) {
	raw := []any{
		map[string]any{
			"role":         "tool",
			"tool_call_id": "call_3",
			"name":         "read_file",
			"content": []any{
				map[string]any{"type": "input_text", "text": "line-1"},
				map[string]any{"type": "output_text", "text": "line-2"},
				map[string]any{"type": "image_url", "image_url": "https://example.com/a.png"},
			},
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	got, _ := normalized[0]["content"].(string)
	if !strings.Contains(got, `line-1`) || !strings.Contains(got, `line-2`) {
		t.Fatalf("expected tool content blocks preserved, got %q", got)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_FunctionRoleCompatible(t *testing.T) {
	raw := []any{
		map[string]any{
			"role":         "function",
			"tool_call_id": "call_4",
			"name":         "legacy_tool",
			"content": map[string]any{
				"ok": true,
			},
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 1 {
		t.Fatalf("expected one normalized message, got %d", len(normalized))
	}
	if normalized[0]["role"] != "tool" {
		t.Fatalf("expected function role normalized as tool, got %#v", normalized[0]["role"])
	}
	got, _ := normalized[0]["content"].(string)
	if !strings.Contains(got, `"ok":true`) || strings.Contains(got, `"name":"legacy_tool"`) {
		t.Fatalf("unexpected normalized function-role content: %q", got)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_EmptyToolContentPreservedAsNull(t *testing.T) {
	raw := []any{
		map[string]any{
			"role":         "tool",
			"tool_call_id": "call_5",
			"name":         "noop_tool",
			"content":      "",
		},
		map[string]any{
			"role":    "assistant",
			"content": "done",
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 2 {
		t.Fatalf("expected tool completion turn to be preserved, got %#v", normalized)
	}
	if normalized[0]["role"] != "tool" {
		t.Fatalf("expected tool role preserved, got %#v", normalized[0]["role"])
	}
	got, _ := normalized[0]["content"].(string)
	if got != "null" {
		t.Fatalf("expected empty tool content normalized as null string, got %q", got)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_DropsInternalTaskToolEvents(t *testing.T) {
	raw := []any{
		map[string]any{"role": "user", "content": "修复 5 个 bug"},
		map[string]any{"role": "tool", "content": "[TaskCreateDone] 'Fix 2: Map Gemini safety/rejection finish reasons' is the second of 5 tasks."},
		map[string]any{"role": "tool", "content": "Task #9 created: Fix 1: Handle functionCall in translateGeminiToClaude"},
		map[string]any{"role": "tool", "content": "Updated task #10 status"},
		map[string]any{"role": "tool", "content": "real command output"},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 2 {
		t.Fatalf("expected internal task tool events to be dropped, got %#v", normalized)
	}
	if got, _ := normalized[1]["content"].(string); got != "real command output" {
		t.Fatalf("expected real tool output preserved, got %q", got)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptScrubsLeakedTaskToolEvents(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "继续修复"},
		map[string]any{"role": "assistant", "content": "● 119] [Tool]\n[TaskCreateDone] 'Fix 2' is the second of 5 tasks.\nUpdated task #10 status\n继续处理 Fix 2"},
		map[string]any{"role": "tool", "content": "Updated task #12 status"},
		map[string]any{"role": "tool", "content": "go test ./... passed"},
	}

	transcript := BuildOpenAICurrentInputContextTranscript(messages)
	for _, leaked := range []string{"[TaskCreateDone]", "Updated task #10 status", "Updated task #12 status", "[Tool]\n[TaskCreateDone]", "● 119] [Tool]"} {
		if strings.Contains(transcript, leaked) {
			t.Fatalf("expected internal task event %q to be scrubbed, got %q", leaked, transcript)
		}
	}
	if !strings.Contains(transcript, "继续处理 Fix 2") || !strings.Contains(transcript, "go test ./... passed") {
		t.Fatalf("expected useful assistant/tool content preserved, got %q", transcript)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptScrubsTooluTaskEvents(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "分析 Gemini 链路"},
		map[string]any{"role": "assistant", "content": "● 136] [Tool]\n[toolu_bdrk_016dHDS5Y...] Task #13 created. Use TaskCreate to add new tasks and TaskUpdate to update task status.\n[137] [Assistant]现在我进行全面分析。先读取所有需要验证的文件。\n\n● 138] [Tool]\n[toolu_bdrk_01A5ZPmcT...] Task #13 is now in_progress.\n[139] [Assistant]让我追踪完整的 Gemini → OpenAI → Gemini 双向链路。"},
	}

	transcript := BuildOpenAICurrentInputContextTranscript(messages)
	for _, leaked := range []string{"[Tool]", "toolu_bdrk", "Task #13 created", "Task #13 is now in_progress"} {
		if strings.Contains(transcript, leaked) {
			t.Fatalf("expected toolu task event %q to be scrubbed, got %q", leaked, transcript)
		}
	}
	for _, want := range []string{"现在我进行全面分析", "让我追踪完整的 Gemini"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected useful assistant content %q preserved, got %q", want, transcript)
		}
	}
}

func TestBuildOpenAICurrentInputContextTranscriptScrubsToolTranscriptCodeBlocks(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "分析 Gemini 链路"},
		map[string]any{"role": "assistant", "content": "136] [Tool]\n[toolu_bdrk_016dHDS5Y...] Task #13 created. Use TaskCreate to add new tasks and TaskUpdate to update task status.\n[137] [Assistant]现在我进行全面分析。先读取所有需要验证的文件。\n\n● 140] [Tool]\n  220\n  221   func geminiToolToOpenAI(raw json.RawMessage) json.RawMessage {\n  222           declarations := extractGeminiDeclarations(raw)\n  223           if declarations == nil {\n  224                   return raw\n  225           }\n  249           out := make([]json.RawMessage, 0, len(declarations))\n  [142] [Tool]\n  141   func decodeOpenAIChatStreamChunk(data []byte) (domain.UnifiedChatResponse, error) {\n  142           var chunk openAISSEChunk\n  143           if err := json.Unmarshal(data, &chunk); err != nil {\n  144                   return domain.UnifiedChatResponse{}, err\n  145           }\n● Reading 2 files… (ctrl+o to expand)\n✽ Processing… (5m 18s · ↑ 5.2k tokens)\n  ⎿  Tip: Use /btw to ask a quick side question"},
	}

	transcript := BuildOpenAICurrentInputContextTranscript(messages)
	for _, leaked := range []string{"toolu_bdrk", "func geminiToolToOpenAI", "func decodeOpenAIChatStreamChunk", "openAISSEChunk", "Reading 2 files", "Processing", "Use /btw"} {
		if strings.Contains(transcript, leaked) {
			t.Fatalf("expected tool transcript/code block %q to be scrubbed, got %q", leaked, transcript)
		}
	}
	if !strings.Contains(transcript, "现在我进行全面分析") {
		t.Fatalf("expected assistant text after marker preserved, got %q", transcript)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptScrubsReadUICodeBlockWithoutToolMarker(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "实现 translateGeminiToOpenAI"},
		map[string]any{"role": "assistant", "content": "好的，Fix 2 的 switch case 已添加。现在实现 translateGeminiToOpenAI 函数。\n\n  Read 1 file (ctrl+o to expand)\n\n● 我现在需要写 translateGeminiToOpenAI 函数。先确认插入位置。\n\n● 181] [Tool]\n  640           })\n  641           blockIndex := 0\n  642           text := accumulatedText.String()\n  643           if text != \"\" {\n  644                   _ = emitClaudeSSE(w, \"content_block_start\", map[string]any{\n  700           return nil\n  701   }\n  702\n  703   // ---------------------------------------------------------------------------\n  704   // Fallback: byte copy\n  705   // ---------------------------------------------------------------------------\n\n● Reading 1 file… (ctrl+o to expand)\n  ⎿  internal\\provider\\stream_translator.go"},
	}

	transcript := BuildOpenAICurrentInputContextTranscript(messages)
	for _, leaked := range []string{"Read 1 file", "blockIndex := 0", "accumulatedText", "emitClaudeSSE", "Fallback: byte copy"} {
		if strings.Contains(transcript, leaked) {
			t.Fatalf("expected read UI/code block %q to be scrubbed, got %q", leaked, transcript)
		}
	}
	if !strings.Contains(transcript, "Already read files:\n- internal\\provider\\stream_translator.go") {
		t.Fatalf("expected read UI file path to be preserved in working state, got %q", transcript)
	}
	for _, want := range []string{"好的，Fix 2 的 switch case 已添加", "先确认插入位置"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected assistant prose %q preserved, got %q", want, transcript)
		}
	}
}

func TestBuildOpenAICurrentInputContextTranscriptScrubsEditFailurePayload(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "修复 stream translator"},
		map[string]any{"role": "assistant", "content": "我需要修复 translateGeminiToOpenAI 函数中被截断的部分。\n\n● 257] [Tool]\nString to replace not found in file.\nString:                       if data == \"[DONE]\" {\n                                if !roleEmitted && (accumulatedText.Len() > 0 || len(funcCalls) > 0) {\n                                        writeOpenAISSE(w, flusher, map[string]any{\n                                                \"id\": chunkID, \"object\": \"chat.completion.chunk\", \"created\": created,\n                                                \"model\": model, \"choices\": []map[string]any{{\"index\": 0, \"delta\": map[string]any{\"role\": \"assistant\"}\n                                        })\n                                }\n  ...[truncated]...\n                return nil\n        }"},
	}

	transcript := BuildOpenAICurrentInputContextTranscript(messages)
	for _, leaked := range []string{"String to replace not found", "if data == \"[DONE]\"", "writeOpenAISSE", "...[truncated]"} {
		if strings.Contains(transcript, leaked) {
			t.Fatalf("expected edit failure payload %q to be scrubbed, got %q", leaked, transcript)
		}
	}
	if !strings.Contains(transcript, "我需要修复 translateGeminiToOpenAI") {
		t.Fatalf("expected useful assistant prose preserved, got %q", transcript)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_AssistantMultipleToolCallsRemainSeparated(t *testing.T) {
	raw := []any{
		map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id":   "call_search",
					"type": "function",
					"function": map[string]any{
						"name":      "search_web",
						"arguments": `{"query":"latest ai news"}`,
					},
				},
				map[string]any{
					"id":   "call_eval",
					"type": "function",
					"function": map[string]any{
						"name":      "eval_javascript",
						"arguments": `{"code":"1+1"}`,
					},
				},
			},
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 1 {
		t.Fatalf("expected assistant tool_call-only message preserved, got %#v", normalized)
	}
	content, _ := normalized[0]["content"].(string)
	if strings.Count(content, "<|DSML|invoke name=") != 2 {
		t.Fatalf("expected two preserved tool call blocks, got %q", content)
	}
	if !strings.Contains(content, `<|DSML|invoke name="search_web">`) || !strings.Contains(content, `<|DSML|invoke name="eval_javascript">`) {
		t.Fatalf("expected both tool names in preserved history, got %q", content)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_PreservesConcatenatedToolArguments(t *testing.T) {
	raw := []any{
		map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id": "call_1",
					"function": map[string]any{
						"name":      "search_web",
						"arguments": `{}{"query":"测试工具调用"}`,
					},
				},
			},
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 1 {
		t.Fatalf("expected assistant tool_call-only content preserved, got %#v", normalized)
	}
	content, _ := normalized[0]["content"].(string)
	if !strings.Contains(content, `{}{"query":"测试工具调用"}`) {
		t.Fatalf("expected concatenated tool arguments preserved, got %q", content)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_AssistantToolCallsMissingNameAreDropped(t *testing.T) {
	raw := []any{
		map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id":   "call_missing_name",
					"type": "function",
					"function": map[string]any{
						"arguments": `{"path":"README.MD"}`,
					},
				},
			},
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 0 {
		t.Fatalf("expected assistant tool_calls without text to be dropped when name is missing, got %#v", normalized)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_AssistantNilContentDoesNotInjectNullLiteral(t *testing.T) {
	raw := []any{
		map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{
				map[string]any{
					"id": "call_screenshot",
					"function": map[string]any{
						"name":      "send_file_to_user",
						"arguments": `{"file_path":"/tmp/a.png"}`,
					},
				},
			},
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 1 {
		t.Fatalf("expected nil-content assistant tool_call-only message preserved, got %#v", normalized)
	}
	content, _ := normalized[0]["content"].(string)
	if strings.Contains(content, "null") {
		t.Fatalf("expected no null literal injection, got %q", content)
	}
	if !strings.Contains(content, "<|DSML|tool_calls>") {
		t.Fatalf("expected assistant tool history in normalized content, got %q", content)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_DeveloperRoleMapsToSystem(t *testing.T) {
	raw := []any{
		map[string]any{"role": "developer", "content": "必须先走工具调用"},
		map[string]any{"role": "user", "content": "你好"},
	}
	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 2 {
		t.Fatalf("expected 2 normalized messages, got %d", len(normalized))
	}
	if normalized[0]["role"] != "system" {
		t.Fatalf("expected developer role converted to system, got %#v", normalized[0]["role"])
	}
}

func TestNormalizeOpenAIMessagesForPrompt_AssistantArrayContentFallbackWhenTextEmpty(t *testing.T) {
	raw := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "", "content": "工具说明文本"},
			},
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 1 {
		t.Fatalf("expected one normalized message, got %d", len(normalized))
	}
	content, _ := normalized[0]["content"].(string)
	if content != "工具说明文本" {
		t.Fatalf("expected content fallback text preserved, got %q", content)
	}
}

func TestNormalizeOpenAIMessagesForPrompt_AssistantReasoningContentPreserved(t *testing.T) {
	raw := []any{
		map[string]any{
			"role":              "assistant",
			"content":           "visible answer",
			"reasoning_content": "internal reasoning",
		},
	}

	normalized := NormalizeOpenAIMessagesForPrompt(raw, "")
	if len(normalized) != 1 {
		t.Fatalf("expected one normalized assistant message, got %#v", normalized)
	}
	content, _ := normalized[0]["content"].(string)
	if !strings.Contains(content, "[reasoning_content]") {
		t.Fatalf("expected labeled reasoning block in assistant content, got %q", content)
	}
	if !strings.Contains(content, "internal reasoning") {
		t.Fatalf("expected reasoning text in assistant content, got %q", content)
	}
	if !strings.Contains(content, "visible answer") {
		t.Fatalf("expected visible answer in assistant content, got %q", content)
	}
	if reasoningIdx := strings.Index(content, "[reasoning_content]"); reasoningIdx < 0 || reasoningIdx > strings.Index(content, "visible answer") {
		t.Fatalf("expected reasoning block before visible answer, got %q", content)
	}
}
