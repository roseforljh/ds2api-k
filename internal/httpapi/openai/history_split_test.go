package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/promptcompat"
)

func historySplitTestMessages() []any {
	toolCalls := []any{
		map[string]any{
			"name":      "search",
			"arguments": map[string]any{"query": "docs"},
		},
	}
	return []any{
		map[string]any{"role": "system", "content": "system instructions"},
		map[string]any{"role": "user", "content": "first user turn"},
		map[string]any{
			"role":              "assistant",
			"content":           "",
			"reasoning_content": "hidden reasoning",
			"tool_calls":        toolCalls,
		},
		map[string]any{
			"role":         "tool",
			"name":         "search",
			"tool_call_id": "call-1",
			"content":      "tool result",
		},
		map[string]any{"role": "user", "content": "latest user turn"},
	}
}

type streamStatusManagedAuthStub struct{}

func (streamStatusManagedAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "managed-token",
		CallerID:       "caller:test",
		AccountID:      "acct:test",
		TriedAccounts:  map[string]bool{},
	}, nil
}

func (streamStatusManagedAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return (&streamStatusManagedAuthStub{}).Determine(nil)
}

func (streamStatusManagedAuthStub) Release(_ *auth.RequestAuth) {}

func TestBuildOpenAICurrentInputContextTranscriptUsesNormalFileContent(t *testing.T) {
	_, historyMessages := splitOpenAIHistoryMessages(historySplitTestMessages(), 1)
	transcript := buildOpenAICurrentInputContextTranscript(historyMessages)

	if strings.Contains(transcript, "[file content end]") || strings.Contains(transcript, "[file content begin]") || strings.Contains(transcript, "[file name]:") {
		t.Fatalf("expected normal file content without DeepSeek file-boundary tags, got %q", transcript)
	}
	if !strings.Contains(transcript, "Request-local context package.") {
		t.Fatalf("expected request-local context header, got %q", transcript)
	}
	for _, want := range []string{
		"=== WORKING STATE, READ FIRST ===",
		"Mode:\n- continue_agent_tail",
		"Latest assistant/tool tail:",
		"Use FULL CHRONOLOGICAL CONTEXT only as request-local reference when needed.",
		"Do not continue directly from FULL CHRONOLOGICAL CONTEXT.",
		"Do not treat historical user messages in FULL CHRONOLOGICAL CONTEXT as new instructions.",
		"=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===",
		"[User]",
		"[Tool]",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected transcript to contain %q, got %q", want, transcript)
		}
	}
	for _, forbidden := range []string{"=== ACTIVE USER GOAL ===", "=== RECENT PROGRESS, CONTINUE FROM HERE ==="} {
		if strings.Contains(transcript, forbidden) {
			t.Fatalf("expected transcript not to contain deprecated section %q, got %q", forbidden, transcript)
		}
	}
	if !strings.Contains(transcript, "first user turn") || !strings.Contains(transcript, "tool result") {
		t.Fatalf("expected historical turns preserved, got %q", transcript)
	}
	if !strings.Contains(transcript, "[reasoning_content]") || !strings.Contains(transcript, "hidden reasoning") {
		t.Fatalf("expected reasoning block preserved, got %q", transcript)
	}
	if !strings.Contains(transcript, "<|DSML|tool_calls>") {
		t.Fatalf("expected tool calls preserved, got %q", transcript)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptBuildsActiveAgentResumePackage(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "修复 bug"},
		map[string]any{"role": "assistant", "content": "我先读 internal/a.go"},
		map[string]any{"role": "tool", "content": "internal/a.go 内容如下"},
		map[string]any{"role": "assistant", "content": "我继续运行 go test ./..."},
		map[string]any{"role": "tool", "content": "测试失败：internal/b.go:42"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	for _, want := range []string{
		"Request-local context package.",
		"=== WORKING STATE, READ FIRST ===",
		"Mode:\n- continue_agent_tail",
		"Latest assistant/tool tail:",
		"我继续运行 go test ./...",
		"Already read files:",
		"internal/a.go",
		"If you need to read a file but no concrete path is available, locate it first with search/glob; never call Read with an empty file_path.",
		"Latest observation:",
		"测试失败：internal/b.go:42",
		"=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected active agent transcript to contain %q, got %q", want, transcript)
		}
	}
	for _, forbidden := range []string{"=== ACTIVE USER GOAL ===", "=== RECENT PROGRESS, CONTINUE FROM HERE ==="} {
		if strings.Contains(transcript, forbidden) {
			t.Fatalf("expected transcript not to contain deprecated section %q, got %q", forbidden, transcript)
		}
	}
}

func TestBuildOpenAICurrentInputContextTranscriptUsesLatestUserInWorkingState(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "旧问题"},
		map[string]any{"role": "assistant", "content": "旧回答"},
		map[string]any{"role": "user", "content": "最新问题"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	for _, want := range []string{
		"=== WORKING STATE, READ FIRST ===",
		"Mode:\n- answer_latest_user",
		"Latest user message:\n[User]\n最新问题",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected latest user working state %q, got %q", want, transcript)
		}
	}
	if strings.Contains(transcript, "=== ACTIVE USER GOAL ===") || strings.Contains(transcript, "=== RECENT PROGRESS, CONTINUE FROM HERE ===") {
		t.Fatalf("expected working-state-only driver sections, got %q", transcript)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptLatestUserInterruptsAssistantTail(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "先做 A"},
		map[string]any{"role": "assistant", "content": "我准备继续 A"},
		map[string]any{"role": "user", "content": "别做 A 了，改做 B"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)
	workingStart := strings.Index(transcript, "=== WORKING STATE, READ FIRST ===")
	fullStart := strings.Index(transcript, "=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===")
	if workingStart < 0 || fullStart < 0 || fullStart <= workingStart {
		t.Fatalf("expected working and full context sections, got %q", transcript)
	}
	working := transcript[workingStart:fullStart]
	for _, want := range []string{
		"Mode:\n- answer_latest_user",
		"Latest user message:\n[User]\n别做 A 了，改做 B",
		"Answer the latest user message. Do not continue stale assistant/tool working state.",
	} {
		if !strings.Contains(working, want) {
			t.Fatalf("expected latest user to interrupt assistant tail %q, got %q", want, working)
		}
	}
	if strings.Contains(working, "我准备继续 A") {
		t.Fatalf("stale assistant tail must not drive latest-user request, got %q", working)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptUsesAssistantTailWhenLatestAssistantHasToolCalls(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "分析项目"},
		map[string]any{"role": "assistant", "content": "我先读取项目结构"},
		map[string]any{"role": "tool", "content": "listed directories"},
		map[string]any{
			"role":    "assistant",
			"content": "接下来检查配置",
			"tool_calls": []any{
				map[string]any{"id": "call_2", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}},
			},
		},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	for _, want := range []string{
		"Mode:\n- continue_agent_tail",
		"Latest assistant/tool tail:",
		"[Assistant]\n我先读取项目结构",
		"[Tool]\nlisted directories",
		"[Assistant]\n接下来检查配置",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected assistant/tool tail %q, got %q", want, transcript)
		}
	}
	if strings.Contains(transcript, "=== ACTIVE USER GOAL ===") || strings.Contains(transcript, "=== RECENT PROGRESS, CONTINUE FROM HERE ===") {
		t.Fatalf("expected no deprecated driver sections, got %q", transcript)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptContinuesAssistantToolCall(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "继续搜索资料"},
		map[string]any{
			"role":    "assistant",
			"content": "我来搜索最新资料。",
			"tool_calls": []any{
				map[string]any{
					"id":   "call_search",
					"type": "function",
					"function": map[string]any{
						"name":      "search",
						"arguments": `{"query":"最新资料"}`,
					},
				},
			},
		},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	for _, want := range []string{
		"Mode:\n- continue_agent_tail",
		"Latest assistant/tool tail:",
		"[Assistant]\n我来搜索最新资料。",
		"<|DSML|tool_calls>",
		`<|DSML|invoke name="search">`,
		`<|DSML|parameter name="query"><![CDATA[最新资料]]></|DSML|parameter>`,
		"Status:\n- Waiting for tool result",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected active assistant tool-call transcript to contain %q, got %q", want, transcript)
		}
	}
}

func TestBuildOpenAICurrentInputContextTranscriptContinuesToolResult(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "继续搜索资料"},
		map[string]any{
			"role":    "assistant",
			"content": "我来搜索最新资料。",
			"tool_calls": []any{
				map[string]any{
					"id":   "call_search",
					"type": "function",
					"function": map[string]any{
						"name":      "search",
						"arguments": `{"query":"最新资料"}`,
					},
				},
			},
		},
		map[string]any{
			"role":         "tool",
			"tool_call_id": "call_search",
			"name":         "search",
			"content":      "搜索结果\n- 文档 A\n- 文档 B",
		},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	for _, want := range []string{
		"Mode:\n- continue_agent_tail",
		"Latest assistant/tool tail:",
		"[Assistant]\n我来搜索最新资料。",
		"[Tool]\n搜索结果",
		"Status:\n- Reviewing latest tool result",
		"Latest observation:\n- 搜索结果 | - 文档 A | - 文档 B",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected active tool-result transcript to contain %q, got %q", want, transcript)
		}
	}
}

func TestBuildOpenAICurrentInputContextTranscriptDoesNotReviveFinalAssistantAnswer(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "问题 A"},
		map[string]any{"role": "assistant", "content": "这是 A 的最终回答。"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)
	for _, want := range []string{
		"Mode:\n- no_active_working",
		"Latest assistant/tool tail:\n- none",
		"No active assistant task is pending. Do not repeat a completed answer.",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected completed assistant answer not to be active working %q, got %q", want, transcript)
		}
	}
	workingStart := strings.Index(transcript, "=== WORKING STATE, READ FIRST ===")
	fullStart := strings.Index(transcript, "=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===")
	if workingStart < 0 || fullStart < 0 || fullStart <= workingStart {
		t.Fatalf("expected working and full context sections, got %q", transcript)
	}
	working := transcript[workingStart:fullStart]
	if strings.Contains(working, "这是 A 的最终回答。") {
		t.Fatalf("completed assistant answer must not be injected as working state, got %q", working)
	}
	if !strings.Contains(transcript[fullStart:], "这是 A 的最终回答。") {
		t.Fatalf("completed answer may remain in reference-only chronological context, got %q", transcript)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptDoesNotReviveFinalAssistantAnswerAfterToolCall(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "检查问题"},
		map[string]any{
			"role":    "assistant",
			"content": "我先调用工具。",
			"tool_calls": []any{
				map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}},
			},
		},
		map[string]any{"role": "tool", "content": "工具结果", "tool_call_id": "call_1"},
		map[string]any{"role": "assistant", "content": "这是工具后的最终回答。"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	if !strings.Contains(transcript, "Mode:\n- no_active_working") {
		t.Fatalf("expected final assistant answer after tool call to be inactive, got %q", transcript)
	}
	workingStart := strings.Index(transcript, "=== WORKING STATE, READ FIRST ===")
	fullStart := strings.Index(transcript, "=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===")
	if workingStart < 0 || fullStart < 0 || fullStart <= workingStart {
		t.Fatalf("expected working and full context sections, got %q", transcript)
	}
	working := transcript[workingStart:fullStart]
	if strings.Contains(working, "这是工具后的最终回答。") {
		t.Fatalf("final assistant answer must not be injected as working state, got %q", working)
	}
	if !strings.Contains(transcript[fullStart:], "这是工具后的最终回答。") {
		t.Fatalf("final assistant answer may remain in reference-only chronological context, got %q", transcript)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptPreservesReadUIPaths(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "继续修复"},
		map[string]any{"role": "assistant", "content": "先读目标文件。\n\n● Read 1 file (ctrl+o to expand)\n  ⎿  internal\\provider\\stream_translator.go"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	if !strings.Contains(transcript, "Already read files:\n- internal\\provider\\stream_translator.go") {
		t.Fatalf("expected read UI path to be preserved in working state, got %q", transcript)
	}
	if strings.Contains(transcript, "Read 1 file") {
		t.Fatalf("expected Read UI noise to stay scrubbed, got %q", transcript)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptDoesNotTruncateMessages(t *testing.T) {
	longContent := strings.Repeat("A", 7000) + "TAIL_MARKER"
	messages := []any{
		map[string]any{"role": "user", "content": "保留完整历史"},
		map[string]any{"role": "assistant", "content": longContent},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	if !strings.Contains(transcript, longContent) {
		t.Fatalf("expected full message content to be preserved")
	}
	fullContextIndex := strings.Index(transcript, "=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===")
	if fullContextIndex < 0 {
		t.Fatalf("expected full chronological context section, got %q", transcript)
	}
	fullContext := transcript[fullContextIndex:]
	if !strings.Contains(fullContext, longContent) || strings.Contains(fullContext, "...[truncated]...") {
		t.Fatalf("expected full chronological messages not to be truncated")
	}
}

func TestBuildOpenAICurrentInputContextTranscriptSummarizesLatestObservation(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "继续修复"},
		map[string]any{"role": "tool", "content": "line1\nline2\nline3\nline4\nline5\nline6"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)
	latest := latestObservationSection(t, transcript)

	if latest != "line1 | line2 | line3 | line4 | ..." {
		t.Fatalf("expected concise latest observation, got %q", latest)
	}
}

func TestBuildOpenAICurrentInputContextTranscriptUsesLastUserWhenNoLaterProgress(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "旧问题"},
		map[string]any{"role": "assistant", "content": "旧回答"},
		map[string]any{"role": "user", "content": "新问题"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	if !strings.Contains(transcript, "Mode:\n- answer_latest_user") {
		t.Fatalf("expected latest user mode, got %q", transcript)
	}
	if !strings.Contains(transcript, "Latest user message:\n[User]\n新问题") {
		t.Fatalf("expected working state to use latest user, got %q", transcript)
	}
	if strings.Contains(transcript, "=== ACTIVE USER GOAL ===") || strings.Contains(transcript, "=== RECENT PROGRESS, CONTINUE FROM HERE ===") {
		t.Fatalf("expected no deprecated driver sections, got %q", transcript)
	}
}

func latestObservationSection(t *testing.T, transcript string) string {
	t.Helper()
	start := strings.Index(transcript, "Latest observation:\n- ")
	if start < 0 {
		t.Fatalf("expected latest observation section, got %q", transcript)
	}
	start += len("Latest observation:\n- ")
	end := strings.Index(transcript[start:], "\n\nNext action:")
	if end < 0 {
		t.Fatalf("expected next action section after latest observation, got %q", transcript)
	}
	return transcript[start : start+end]
}

func TestBuildOpenAICurrentInputContextTranscriptExtractsChangedFiles(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "修改上下文逻辑"},
		map[string]any{"role": "tool", "content": "*** Begin Patch\n*** Update File: internal/promptcompat/history_transcript.go\n@@\n-old\n+new\n*** End Patch"},
	}
	transcript := buildOpenAICurrentInputContextTranscript(messages)

	if !strings.Contains(transcript, "Already changed files:\n- internal/promptcompat/history_transcript.go") {
		t.Fatalf("expected changed file extraction, got %q", transcript)
	}
}

func TestBuildOpenAIToolPromptFileTranscriptUsesNormalSystemContent(t *testing.T) {
	transcript := promptcompat.BuildOpenAIToolPromptFileTranscript("You have access to these tools:\n\nTool: search")
	for _, want := range []string{
		"Tool instructions for the current request.",
		"Treat the instructions below as active system-level tool instructions.",
		"[System]",
		"You have access to these tools:",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected tool prompt transcript to contain %q, got %q", want, transcript)
		}
	}
	if strings.Contains(transcript, "[file content end]") || strings.Contains(transcript, "[file content begin]") || strings.Contains(transcript, "[file name]:") || strings.Contains(transcript, "00_AGENT_TOOLS") || strings.Contains(transcript, "TOOL_PROMPT") {
		t.Fatalf("expected normal tool prompt file content without file-boundary tags or upload names, got %q", transcript)
	}
}

func TestSplitOpenAIHistoryMessagesUsesLatestUserTurn(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "system instructions"},
		map[string]any{"role": "user", "content": "first user turn"},
		map[string]any{"role": "assistant", "content": "first assistant turn"},
		map[string]any{"role": "user", "content": "middle user turn"},
		map[string]any{"role": "assistant", "content": "middle assistant turn"},
		map[string]any{"role": "user", "content": "latest user turn"},
	}

	promptMessages, historyMessages := splitOpenAIHistoryMessages(messages, 1)
	if len(promptMessages) == 0 || len(historyMessages) == 0 {
		t.Fatalf("expected both prompt and history messages, got prompt=%d history=%d", len(promptMessages), len(historyMessages))
	}

	promptText, _ := promptcompat.BuildOpenAIPrompt(promptMessages, nil, "", defaultToolChoicePolicy(), true)
	if !strings.Contains(promptText, "latest user turn") {
		t.Fatalf("expected latest user turn in prompt, got %s", promptText)
	}
	if strings.Contains(promptText, "middle user turn") {
		t.Fatalf("expected middle user turn to be moved into history, got %s", promptText)
	}

	historyText := buildOpenAICurrentInputContextTranscript(historyMessages)
	if !strings.Contains(historyText, "middle user turn") {
		t.Fatalf("expected middle user turn in split history, got %s", historyText)
	}
	if strings.Contains(historyText, "latest user turn") {
		t.Fatalf("expected latest user turn to remain live, got %s", historyText)
	}
}

func TestApplyCurrentInputFileSkipsShortInputWhenThresholdNotReached(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     10,
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 0 {
		t.Fatalf("expected no upload on first turn, got %d", len(ds.uploadCalls))
	}
	if out.FinalPrompt != stdReq.FinalPrompt {
		t.Fatalf("expected prompt unchanged on first turn")
	}
}

func TestApplyThinkingInjectionAppendsLatestUserPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:         true,
			thinkingInjection: boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply thinking injection failed: %v", err)
	}
	if len(ds.uploadCalls) != 0 {
		t.Fatalf("expected no upload for first short turn, got %d", len(ds.uploadCalls))
	}
	if !strings.Contains(out.FinalPrompt, "hello\n\n"+promptcompat.ThinkingInjectionMarker) {
		t.Fatalf("expected thinking injection after latest user message, got %s", out.FinalPrompt)
	}
}

func TestApplyThinkingInjectionUsesCustomPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:         true,
			thinkingInjection: boolPtr(true),
			thinkingPrompt:    "custom thinking format",
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply thinking injection failed: %v", err)
	}
	if !strings.Contains(out.FinalPrompt, "hello\n\ncustom thinking format") {
		t.Fatalf("expected custom thinking injection after latest user message, got %s", out.FinalPrompt)
	}
}

func TestApplyCurrentInputFileDisabledPassThrough(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: false,
		},
		DS: ds,
	}
	req := map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 0 {
		t.Fatalf("expected no uploads when both split modes are disabled, got %d", len(ds.uploadCalls))
	}
	if out.CurrentInputFileApplied || out.HistoryText != "" {
		t.Fatalf("expected direct pass-through, got current_input=%v history=%q", out.CurrentInputFileApplied, out.HistoryText)
	}
	if !strings.Contains(out.FinalPrompt, "first user turn") || !strings.Contains(out.FinalPrompt, "latest user turn") {
		t.Fatalf("expected original prompt context to stay inline, got %s", out.FinalPrompt)
	}
}

func TestApplyCurrentInputFileUploadsFirstTurnWithInjectedWrapper(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     10,
			thinkingInjection:   boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "first turn content that is long enough"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 current input upload, got %d", len(ds.uploadCalls))
	}
	upload := ds.uploadCalls[0]
	if upload.Filename != "HISTORY.txt" {
		t.Fatalf("unexpected upload filename: %q", upload.Filename)
	}
	uploadedText := string(upload.Data)
	if !bytes.HasPrefix(upload.Data, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("expected UTF-8 BOM prefix on generated context upload, got % x", upload.Data[:min(3, len(upload.Data))])
	}
	if strings.Contains(uploadedText, "[file content end]") || strings.Contains(uploadedText, "[file content begin]") || strings.Contains(uploadedText, "[file name]:") {
		t.Fatalf("expected normal current input file content without file-boundary tags, got %q", uploadedText)
	}
	if !strings.Contains(uploadedText, "[User]\nfirst turn content that is long enough") {
		t.Fatalf("expected readable current user turn, got %q", uploadedText)
	}
	if !strings.Contains(uploadedText, promptcompat.ThinkingInjectionMarker) {
		t.Fatalf("expected thinking injection in current input file, got %q", uploadedText)
	}
	if strings.Contains(out.FinalPrompt, "first turn content that is long enough") {
		t.Fatalf("expected current input text to be replaced in live prompt, got %s", out.FinalPrompt)
	}
	if strings.Contains(out.FinalPrompt, "CURRENT_USER_INPUT.txt") || strings.Contains(out.FinalPrompt, "Read that file") {
		t.Fatalf("expected live prompt not to use deprecated file-read wording, got %s", out.FinalPrompt)
	}
	if !strings.Contains(out.FinalPrompt, "current API request") {
		t.Fatalf("expected neutral continuation instruction in live prompt, got %s", out.FinalPrompt)
	}
	if !strings.Contains(out.FinalPrompt, "HISTORY.txt") {
		t.Fatalf("expected final prompt to mention HISTORY.txt, got %s", out.FinalPrompt)
	}
	if !strings.Contains(out.FinalPrompt, "follow its WORKING STATE section") {
		t.Fatalf("expected final prompt to instruct following HISTORY.txt working state, got %s", out.FinalPrompt)
	}
	if !strings.Contains(out.FinalPrompt, "no_active_working") {
		t.Fatalf("expected final prompt to mention no_active_working, got %s", out.FinalPrompt)
	}
	if len(out.RefFileIDs) != 1 || out.RefFileIDs[0] != "file-inline-1" {
		t.Fatalf("expected current input file id in ref_file_ids, got %#v", out.RefFileIDs)
	}
}

func TestApplyCurrentInputFileUploadsFullContextFile(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     0,
			thinkingInjection:   boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if !out.CurrentInputFileApplied {
		t.Fatalf("expected current input file to apply")
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected one current input upload, got %d", len(ds.uploadCalls))
	}
	upload := ds.uploadCalls[0]
	if upload.Filename != "HISTORY.txt" {
		t.Fatalf("expected HISTORY.txt upload, got %q", upload.Filename)
	}
	uploadedText := string(upload.Data)
	for _, want := range []string{"system instructions", "first user turn", "hidden reasoning", "tool result", "latest user turn", promptcompat.ThinkingInjectionMarker} {
		if !strings.Contains(uploadedText, want) {
			t.Fatalf("expected full context file to contain %q, got %q", want, uploadedText)
		}
	}
	if strings.Contains(out.FinalPrompt, "first user turn") || strings.Contains(out.FinalPrompt, "latest user turn") || strings.Contains(out.FinalPrompt, "CURRENT_USER_INPUT.txt") || strings.Contains(out.FinalPrompt, "Read that file") {
		t.Fatalf("expected live prompt to use only a neutral continuation instruction, got %s", out.FinalPrompt)
	}
	if !strings.Contains(out.FinalPrompt, "current API request") {
		t.Fatalf("expected neutral continuation instruction in live prompt, got %s", out.FinalPrompt)
	}
}

func TestApplyCurrentInputFileDropsStaleRefFileIDs(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     0,
			thinkingInjection:   boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model":        "deepseek-v4-flash",
		"ref_file_ids": []any{"stale-history-file"},
		"messages": []any{
			map[string]any{"role": "user", "content": "latest user turn"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(out.RefFileIDs) != 1 || out.RefFileIDs[0] != "file-inline-1" {
		t.Fatalf("expected only current HISTORY file id, got %#v", out.RefFileIDs)
	}
}

func TestApplyCurrentInputFileUsesRequestLocalPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     0,
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if strings.Contains(strings.ToLower(out.FinalPrompt), "resume") ||
		strings.Contains(strings.ToLower(out.FinalPrompt), "continue from") ||
		strings.Contains(strings.ToLower(out.FinalPrompt), "do not restart") {
		t.Fatalf("expected request-local neutral prompt, got %q", out.FinalPrompt)
	}
	for _, want := range []string{"current API request", "latest user message", "previous sessions"} {
		if !strings.Contains(out.FinalPrompt, want) {
			t.Fatalf("expected final prompt to contain %q, got %q", want, out.FinalPrompt)
		}
	}
	for _, want := range []string{
		"If the latest user message explicitly asks to continue prior work",
		"Otherwise, answer the latest user message directly.",
	} {
		if !strings.Contains(out.FinalPrompt, want) {
			t.Fatalf("expected revised neutral prompt to contain %q, got %q", want, out.FinalPrompt)
		}
	}
	if strings.Contains(out.FinalPrompt, "standalone") || strings.Contains(out.FinalPrompt, "instead of continuing prior work") {
		t.Fatalf("expected old standalone continuation wording to be removed, got %q", out.FinalPrompt)
	}
}

func TestApplyCurrentInputFileUploadsToolPromptFileWhenEnabled(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     0,
			toolPromptFile:      true,
		},
		DS: ds,
	}
	req := map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "search",
					"description": "Search docs",
					"parameters": map[string]any{
						"type": "object",
					},
				},
			},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 2 {
		t.Fatalf("expected context and tool prompt uploads, got %d", len(ds.uploadCalls))
	}
	if ds.uploadCalls[0].Filename != "HISTORY.txt" {
		t.Fatalf("expected first upload to stay HISTORY.txt, got %q", ds.uploadCalls[0].Filename)
	}
	if ds.uploadCalls[1].Filename != "TOOL_PROMPT.txt" {
		t.Fatalf("expected second upload to use non-context .txt tool filename, got %q", ds.uploadCalls[1].Filename)
	}
	toolText := string(ds.uploadCalls[1].Data)
	if !bytes.HasPrefix(ds.uploadCalls[1].Data, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("expected UTF-8 BOM prefix on generated tool prompt upload, got % x", ds.uploadCalls[1].Data[:min(3, len(ds.uploadCalls[1].Data))])
	}
	if !strings.Contains(toolText, "You have access to these tools:") || !strings.Contains(toolText, "Tool: search") {
		t.Fatalf("expected tool prompt upload to contain tool instructions, got %q", toolText)
	}
	if strings.Contains(toolText, "[file content end]") || strings.Contains(toolText, "[file content begin]") || strings.Contains(toolText, "[file name]:") || strings.Contains(toolText, "00_AGENT_TOOLS") || strings.Contains(toolText, "TOOL_PROMPT") {
		t.Fatalf("expected normal tool prompt upload without file-boundary tags or upload names, got %q", toolText)
	}
	if strings.Contains(out.FinalPrompt, "You have access to these tools:") {
		t.Fatalf("expected final prompt not to inline tool prompt, got %s", out.FinalPrompt)
	}
	if strings.Contains(out.FinalPrompt, "00_AGENT_TOOLS") || strings.Contains(out.FinalPrompt, "Read that file") {
		t.Fatalf("expected final prompt not to reference concrete tool/context files, got %s", out.FinalPrompt)
	}
	for _, want := range []string{
		"Before emitting any tool call, read TOOL_PROMPT.txt and follow its exact tool-call syntax.",
		"When emitting a tool call, output only the tool call and no additional prose before or after it.",
		"<|DSML|tool_calls>",
		"Never use SML_DOLLAR_EM_OLLAR_",
		"HISTORY.txt",
		"follow its WORKING STATE section",
		"no_active_working",
		"If the latest user message explicitly asks to continue prior work",
		"Otherwise, answer the latest user message directly.",
	} {
		if !strings.Contains(out.FinalPrompt, want) {
			t.Fatalf("expected final prompt to contain tool prompt anchor %q, got %s", want, out.FinalPrompt)
		}
	}
	for _, old := range []string{"Do not call tools until you have applied TOOL_PROMPT.txt.", "If the latest user message is standalone"} {
		if strings.Contains(out.FinalPrompt, old) {
			t.Fatalf("expected old tool prompt wording %q to be removed, got %s", old, out.FinalPrompt)
		}
	}
	if strings.Contains(out.FinalPrompt, "Tool: search") {
		t.Fatalf("expected final prompt to avoid full tool schema inlining, got %s", out.FinalPrompt)
	}
	if !strings.Contains(out.FinalPrompt, "current API request") || !strings.Contains(out.FinalPrompt, "active tool instructions") {
		t.Fatalf("expected final prompt to activate attached tool instructions, got %s", out.FinalPrompt)
	}
	if len(out.RefFileIDs) < 2 || out.RefFileIDs[0] != "file-inline-2" || out.RefFileIDs[1] != "file-inline-1" {
		t.Fatalf("expected tool file before history file in ref ids, got %#v", out.RefFileIDs)
	}
	if len(out.ToolNames) != 1 || out.ToolNames[0] != "search" {
		t.Fatalf("expected tool names to be preserved, got %#v", out.ToolNames)
	}
}

func TestApplyCurrentInputFileExposesHistoryTextForPersistence(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		DS: ds,
	}
	req := map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(ds.uploadCalls))
	}
	if !strings.Contains(out.HistoryText, "latest user turn") {
		t.Fatalf("expected current input file flow to expose uploaded history text for persistence, got %q", out.HistoryText)
	}
}

func TestChatCompletionsCurrentInputFileUploadsContextAndKeepsNeutralPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(ds.uploadCalls))
	}
	upload := ds.uploadCalls[0]
	if upload.Filename != "HISTORY.txt" {
		t.Fatalf("unexpected upload filename: %q", upload.Filename)
	}
	if upload.Purpose != "assistants" {
		t.Fatalf("unexpected purpose: %q", upload.Purpose)
	}
	historyText := string(upload.Data)
	if strings.Contains(historyText, "[file content end]") || strings.Contains(historyText, "[file content begin]") || strings.Contains(historyText, "[file name]:") {
		t.Fatalf("expected normal current input file content without file-boundary tags, got %s", historyText)
	}
	if !strings.Contains(historyText, "latest user turn") {
		t.Fatalf("expected full context to include latest turn, got %s", historyText)
	}
	if ds.completionReq == nil {
		t.Fatal("expected completion payload to be captured")
	}
	promptText, _ := ds.completionReq["prompt"].(string)
	if !strings.Contains(promptText, "current API request") {
		t.Fatalf("expected neutral completion prompt, got %s", promptText)
	}
	if strings.Contains(promptText, "first user turn") || strings.Contains(promptText, "latest user turn") {
		t.Fatalf("expected prompt to hide original turns, got %s", promptText)
	}
	refIDs, _ := ds.completionReq["ref_file_ids"].([]any)
	if len(refIDs) == 0 || refIDs[0] != "file-inline-1" {
		t.Fatalf("expected uploaded current input file to be first ref_file_id, got %#v", ds.completionReq["ref_file_ids"])
	}
}

func TestResponsesCurrentInputFileUploadsContextAndKeepsNeutralPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(ds.uploadCalls))
	}
	if ds.completionReq == nil {
		t.Fatal("expected completion payload to be captured")
	}
	promptText, _ := ds.completionReq["prompt"].(string)
	if !strings.Contains(promptText, "current API request") {
		t.Fatalf("expected neutral completion prompt, got %s", promptText)
	}
	if strings.Contains(promptText, "first user turn") || strings.Contains(promptText, "latest user turn") {
		t.Fatalf("expected prompt to hide original turns, got %s", promptText)
	}
}

func TestChatCompletionsCurrentInputFileMapsManagedAuthFailureTo401(t *testing.T) {
	ds := &inlineUploadDSStub{
		uploadErr: &dsclient.RequestFailure{Op: "upload file", Kind: dsclient.FailureManagedUnauthorized, Message: "expired token"},
	}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusManagedAuthStub{},
		DS:   ds,
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer managed-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Please re-login the account in admin") {
		t.Fatalf("expected managed auth error message, got %s", rec.Body.String())
	}
}

func TestResponsesCurrentInputFileMapsDirectAuthFailureTo401(t *testing.T) {
	ds := &inlineUploadDSStub{
		uploadErr: &dsclient.RequestFailure{Op: "upload file", Kind: dsclient.FailureDirectUnauthorized, Message: "invalid token"},
	}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid token") {
		t.Fatalf("expected direct auth error message, got %s", rec.Body.String())
	}
}

func TestChatCompletionsCurrentInputFileUploadFailureReturnsInternalServerError(t *testing.T) {
	ds := &inlineUploadDSStub{uploadErr: errors.New("boom")}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCurrentInputFileWorksAcrossAutoDeleteModes(t *testing.T) {
	for _, mode := range []string{"none", "single", "all"} {
		t.Run(mode, func(t *testing.T) {
			ds := &inlineUploadDSStub{}
			h := &openAITestSurface{
				Store: mockOpenAIConfig{
					wideInput:           true,
					autoDeleteMode:      mode,
					currentInputEnabled: true,
				},
				Auth: streamStatusAuthStub{},
				DS:   ds,
			}
			reqBody, _ := json.Marshal(map[string]any{
				"model":    "deepseek-v4-flash",
				"messages": historySplitTestMessages(),
				"stream":   false,
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
			req.Header.Set("Authorization", "Bearer direct-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			h.ChatCompletions(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if len(ds.uploadCalls) != 1 {
				t.Fatalf("expected current input upload for mode=%s, got %d", mode, len(ds.uploadCalls))
			}
			if ds.completionReq == nil {
				t.Fatalf("expected completion payload for mode=%s", mode)
			}
			promptText, _ := ds.completionReq["prompt"].(string)
			if !strings.Contains(promptText, "current API request") || strings.Contains(promptText, "first user turn") || strings.Contains(promptText, "latest user turn") {
				t.Fatalf("unexpected prompt for mode=%s: %s", mode, promptText)
			}
		})
	}
}

func defaultToolChoicePolicy() promptcompat.ToolChoicePolicy {
	return promptcompat.DefaultToolChoicePolicy()
}

func boolPtr(v bool) *bool {
	return &v
}
