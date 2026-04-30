package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/chathistory"
	"ds2api/internal/promptcompat"
)

func newTestChatHistoryStore(t *testing.T) *chathistory.Store {
	t.Helper()
	store := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	if err := store.Err(); err != nil {
		t.Fatalf("chat history store unavailable: %v", err)
	}
	return store
}

func blockChatHistoryDetailDir(t *testing.T, detailDir string) func() {
	t.Helper()
	blockedDir := detailDir + ".blocked"
	if err := os.RemoveAll(blockedDir); err != nil {
		t.Fatalf("remove blocked detail dir failed: %v", err)
	}
	if err := os.Rename(detailDir, blockedDir); err != nil {
		t.Fatalf("move detail dir aside failed: %v", err)
	}
	if err := os.RemoveAll(detailDir); err != nil {
		t.Fatalf("remove blocked detail path failed: %v", err)
	}
	if err := os.WriteFile(detailDir, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("write blocked detail path failed: %v", err)
	}
	var once sync.Once
	return func() {
		t.Helper()
		once.Do(func() {
			if err := os.RemoveAll(detailDir); err != nil {
				t.Fatalf("remove blocking detail path failed: %v", err)
			}
			if err := os.Rename(blockedDir, detailDir); err != nil {
				t.Fatalf("restore detail dir failed: %v", err)
			}
		})
	}
}

func TestChatCompletionsNonStreamPersistsHistory(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	h := &Handler{
		Store:       mockOpenAIConfig{wideInput: true},
		Auth:        streamStatusAuthStub{},
		DS:          streamStatusDSStub{resp: makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello world"}`, `data: [DONE]`)},
		ChatHistory: historyStore,
	}

	reqBody := `{"model":"deepseek-v4-flash","messages":[{"role":"system","content":"be precise"},{"role":"user","content":"hi there"},{"role":"assistant","content":"previous answer"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	item := snapshot.Items[0]
	if item.Status != "success" || item.UserInput != "hi there" {
		t.Fatalf("unexpected persisted history summary: %#v", item)
	}
	full, err := historyStore.Get(item.ID)
	if err != nil {
		t.Fatalf("expected detail item, got %v", err)
	}
	if full.Content != "hello world" {
		t.Fatalf("expected detail content persisted, got %#v", full)
	}
	if len(full.Messages) != 3 {
		t.Fatalf("expected all request messages persisted, got %#v", full.Messages)
	}
	if full.FinalPrompt == "" {
		t.Fatalf("expected final prompt to be persisted")
	}
	if item.CallerID != "caller:test" {
		t.Fatalf("expected caller hash persisted in summary, got %#v", item.CallerID)
	}
}

func TestStartChatHistoryRecoversFromTransientWriteFailure(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	restore := blockChatHistoryDetailDir(t, historyStore.DetailDir())
	t.Cleanup(restore)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	a := &auth.RequestAuth{
		CallerID:  "caller:test",
		AccountID: "acct:test",
	}
	stdReq := promptcompat.StandardRequest{
		ResponseModel: "deepseek-v4-flash",
		Stream:        true,
		Messages: []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		FinalPrompt: "hello",
	}

	session := startChatHistory(historyStore, req, a, stdReq)
	if session == nil {
		t.Fatalf("expected session even when initial persistence fails")
	}
	if session.disabled {
		t.Fatalf("expected session to remain active after transient start failure")
	}
	if session.entryID == "" {
		t.Fatalf("expected session entry id to be retained")
	}
	if err := historyStore.Err(); err != nil {
		t.Fatalf("transient start failure should not latch store error: %v", err)
	}

	session.lastPersist = time.Now().Add(-time.Second)
	session.progress("thinking", "partial")
	if session.disabled {
		t.Fatalf("expected session to remain active after transient update failure")
	}
	if session.entryID == "" {
		t.Fatalf("expected session entry id to remain set after update failure")
	}
	if err := historyStore.Err(); err != nil {
		t.Fatalf("transient update failure should not latch store error: %v", err)
	}

	restore()

	session.success(http.StatusOK, "thinking", "final answer", "stop", map[string]any{"total_tokens": 7})
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed after restore: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one persisted item after restore, got %#v", snapshot.Items)
	}
	full, err := historyStore.Get(session.entryID)
	if err != nil {
		t.Fatalf("get restored entry failed: %v", err)
	}
	if full.Status != "success" || full.Content != "final answer" {
		t.Fatalf("expected restored entry to persist final success, got %#v", full)
	}
}

func TestHandleStreamContextCancelledMarksHistoryStopped(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	entry, err := historyStore.Start(chathistory.StartParams{
		CallerID:  "caller:test",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start history failed: %v", err)
	}
	session := &chatHistorySession{
		store:       historyStore,
		entryID:     entry.ID,
		startedAt:   time.Now(),
		lastPersist: time.Now(),
		finalPrompt: "hello",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	resp := makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello"}`, `data: [DONE]`)

	h.handleStream(rec, req, resp, "cid-stop", "deepseek-v4-flash", "prompt", false, false, nil, nil, session)

	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	full, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if full.Status != "stopped" {
		t.Fatalf("expected stopped status, got %#v", full)
	}
}

func TestHandleStreamWithRetryContextCancelledDoesNotOverwriteStoppedHistory(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	entry, err := historyStore.Start(chathistory.StartParams{
		CallerID:  "caller:test",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start history failed: %v", err)
	}
	session := &chatHistorySession{
		store:       historyStore,
		entryID:     entry.ID,
		startedAt:   time.Now(),
		lastPersist: time.Now(),
		finalPrompt: "hello",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	resp := makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello"}`, `data: [DONE]`)
	streamRuntime := newChatStreamRuntime(
		rec,
		http.NewResponseController(rec),
		false,
		"cid-stop",
		time.Now().Unix(),
		"deepseek-v4-flash",
		"hello",
		false,
		false,
		true,
		nil,
		nil,
		false,
		false,
	)

	h.consumeChatStreamAttempt(req, resp, streamRuntime, "text", false, session, false)

	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	full, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if full.Status != "stopped" {
		t.Fatalf("expected stopped status to remain after retry stream cancellation, got %#v", full)
	}
}

func TestChatCompletionsCapturesAdminWebUISource(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	h := &Handler{
		Store:       mockOpenAIConfig{wideInput: true},
		Auth:        streamStatusAuthStub{},
		DS:          streamStatusDSStub{resp: makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello world"}`, `data: [DONE]`)},
		ChatHistory: historyStore,
	}

	reqBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi there"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(adminWebUISourceHeader, adminWebUISourceValue)
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected admin webui source to be captured, got %#v", snapshot.Items)
	}
	if snapshot.Items[0].UserInput != "hi there" {
		t.Fatalf("unexpected captured user input: %#v", snapshot.Items[0])
	}
}

func TestChatCompletionsCapturesHistoryAfterLimitReset(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	if _, err := historyStore.SetLimit(chathistory.DisabledLimit); err != nil {
		t.Fatalf("reset history limit failed: %v", err)
	}
	h := &Handler{
		Store:       mockOpenAIConfig{wideInput: true},
		Auth:        streamStatusAuthStub{},
		DS:          streamStatusDSStub{resp: makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello world"}`, `data: [DONE]`)},
		ChatHistory: historyStore,
	}

	reqBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi there"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected history to capture first item after limit reset, got %#v", snapshot.Items)
	}
}

func TestChatCompletionsCurrentInputFilePersistsNeutralMessagesAndHistoryText(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	ds := &inlineUploadDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth:        streamStatusAuthStub{},
		DS:          ds,
		ChatHistory: historyStore,
	}

	reqBody := `{"model":"deepseek-v4-flash","messages":[{"role":"system","content":"system instructions"},{"role":"user","content":"first user turn"},{"role":"assistant","content":"","reasoning_content":"hidden reasoning","tool_calls":[{"name":"search","arguments":{"query":"docs"}}]},{"role":"tool","name":"search","tool_call_id":"call-1","content":"tool result"},{"role":"user","content":"latest user turn"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	full, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("expected detail item, got %v", err)
	}
	if !strings.Contains(full.HistoryText, "latest user turn") || !strings.Contains(full.HistoryText, "first user turn") {
		t.Fatalf("expected current input file flow to persist uploaded history text, got %q", full.HistoryText)
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected current input upload to happen, got %d", len(ds.uploadCalls))
	}
	if ds.uploadCalls[0].Filename != "HISTORY.txt" {
		t.Fatalf("expected HISTORY.txt upload, got %q", ds.uploadCalls[0].Filename)
	}
	if full.UserInput != "latest user turn" {
		t.Fatalf("expected latest real user input to be persisted, got %q", full.UserInput)
	}
	if strings.Contains(full.UserInput, "HISTORY.txt") || strings.Contains(full.UserInput, "TOOL INSTRUCTIONS") {
		t.Fatalf("synthetic current-input prompt leaked into user input: %q", full.UserInput)
	}
	if len(full.Messages) != 4 {
		t.Fatalf("expected visible original request messages to be persisted, got %#v", full.Messages)
	}
	for _, msg := range full.Messages {
		if strings.Contains(msg.Content, "HISTORY.txt") || strings.Contains(msg.Content, "TOOL INSTRUCTIONS") {
			t.Fatalf("synthetic prompt leaked into persisted messages: %#v", full.Messages)
		}
	}
	if !strings.Contains(full.FinalPrompt, "HISTORY.txt") || !strings.Contains(full.FinalPrompt, "WORKING STATE") {
		t.Fatalf("expected live prompt to be persisted for official-web style history, got %q", full.FinalPrompt)
	}
}

func TestChatCompletionsHistoryHidesInlineToolPromptFromWebUI(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	ds := &inlineUploadDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			toolPromptFile:      true,
		},
		Auth:        streamStatusAuthStub{},
		DS:          ds,
		ChatHistory: historyStore,
	}

	reqBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"find docs"},{"role":"assistant","content":"","tool_calls":[{"name":"search","arguments":{"query":"docs"}}]},{"role":"tool","name":"search","tool_call_id":"call-1","content":"tool result"},{"role":"user","content":"summarize"}],"tools":[{"type":"function","function":{"name":"search","description":"Search docs","parameters":{"type":"object"}}}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	upstreamPrompt, _ := ds.completionReq["prompt"].(string)
	if !strings.Contains(upstreamPrompt, "=== TOOL INSTRUCTIONS, MUST FOLLOW ===") || !strings.Contains(upstreamPrompt, "Tool: search") {
		t.Fatalf("expected upstream prompt to keep inline tool instructions, got %q", upstreamPrompt)
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	full, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("expected detail item, got %v", err)
	}
	if strings.Contains(full.FinalPrompt, "=== TOOL INSTRUCTIONS, MUST FOLLOW ===") || strings.Contains(full.FinalPrompt, "Tool: search") || strings.Contains(full.FinalPrompt, "You have access to these tools:") {
		t.Fatalf("expected WebUI final prompt to hide injected tool instructions, got %q", full.FinalPrompt)
	}
	if !strings.Contains(full.FinalPrompt, "HISTORY.txt") || !strings.Contains(full.FinalPrompt, "WORKING STATE") {
		t.Fatalf("expected WebUI final prompt to keep live prompt context instruction, got %q", full.FinalPrompt)
	}
	if full.ToolPromptText != "" {
		t.Fatalf("expected tool prompt text not to be persisted for WebUI, got %q", full.ToolPromptText)
	}
}
