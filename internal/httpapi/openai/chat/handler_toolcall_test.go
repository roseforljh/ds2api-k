package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
)

func makeSSEHTTPResponse(lines ...string) *http.Response {
	body := strings.Join(lines, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func decodeJSONBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode json failed: %v, body=%s", err, body)
	}
	return out
}

func parseSSEDataFrames(t *testing.T, body string) ([]map[string]any, bool) {
	t.Helper()
	lines := strings.Split(body, "\n")
	frames := make([]map[string]any, 0, len(lines))
	done := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			done = true
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			t.Fatalf("decode sse frame failed: %v, payload=%s", err, payload)
		}
		frames = append(frames, frame)
	}
	return frames, done
}

func streamHasToolCallsDelta(frames []map[string]any) bool {
	for _, frame := range frames {
		choices, _ := frame["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if _, ok := delta["tool_calls"]; ok {
				return true
			}
		}
	}
	return false
}

func streamFinishReason(frames []map[string]any) string {
	for _, frame := range frames {
		choices, _ := frame["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
				return reason
			}
		}
	}
	return ""
}

// Backward-compatible alias for historical test name used in CI logs.
func TestHandleNonStreamReturns429WhenUpstreamOutputEmpty(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/content","v":""}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()

	h.handleNonStream(rec, resp, "cid-empty", "deepseek-v4-flash", "prompt", false, false, nil, nil, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429 for empty upstream output, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if asString(errObj["code"]) != "upstream_empty_output" {
		t.Fatalf("expected code=upstream_empty_output, got %#v", out)
	}
}

func TestHandleNonStreamReturnsContentFilterErrorWhenUpstreamFilteredWithoutOutput(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"code":"content_filter"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()

	h.handleNonStream(rec, resp, "cid-empty-filtered", "deepseek-v4-flash", "prompt", false, false, nil, nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for filtered upstream output, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if asString(errObj["code"]) != "content_filter" {
		t.Fatalf("expected code=content_filter, got %#v", out)
	}
}

func TestHandleNonStreamReturns429WhenUpstreamHasOnlyThinking(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/thinking_content","v":"Only thinking"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()

	h.handleNonStream(rec, resp, "cid-thinking-only", "deepseek-v4-pro", "prompt", true, false, nil, nil, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429 for thinking-only upstream output, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if asString(errObj["code"]) != "upstream_empty_output" {
		t.Fatalf("expected code=upstream_empty_output, got %#v", out)
	}
}

func TestHandleNonStreamPromotesThinkingToolCallsWhenTextEmpty(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/thinking_content","v":"<tool_calls><invoke name=\"search\"><parameter name=\"q\">from-thinking</parameter></invoke></tool_calls>"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()

	h.handleNonStream(rec, resp, "cid-thinking-tool", "deepseek-v4-pro", "prompt", true, false, []string{"search"}, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for thinking tool calls, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("expected choices, got %#v", out)
	}
	choice, _ := choices[0].(map[string]any)
	if got := asString(choice["finish_reason"]); got != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, got %#v", choice["finish_reason"])
	}
	message, _ := choice["message"].(map[string]any)
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", message["tool_calls"])
	}
	if content, exists := message["content"]; !exists || content != nil {
		t.Fatalf("expected content nil when tool call promoted, got %#v", message["content"])
	}
}

func TestHandleNonStreamPromotesHiddenThinkingDSMLToolCallsWhenTextEmpty(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/thinking_content","v":"<|DSML|tool_calls><|DSML|invoke name=\"search\"><|DSML|parameter name=\"q\">from-hidden-thinking</|DSML|parameter></|DSML|invoke></|DSML|tool_calls>"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()

	h.handleNonStream(rec, resp, "cid-hidden-thinking-tool", "deepseek-v4-pro", "prompt", false, false, []string{"search"}, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for hidden thinking tool calls, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	choices, _ := out["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, ok := message["reasoning_content"]; ok {
		t.Fatalf("expected hidden thinking not to be exposed, got %#v", message)
	}
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("expected one hidden-thinking tool call, got %#v", message["tool_calls"])
	}
	if got := asString(choice["finish_reason"]); got != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, got %#v", choice["finish_reason"])
	}
}

func TestHandleNonStreamPromotesDSMFullwidthToolCalls(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/content","v":"<DSM｜tool_calls><DSM｜invoke name=\"Bash\"><DSM｜parameter name=\"command\"><![CDATA[pwd]]></DSM｜parameter></DSM｜invoke></DSM｜tool_calls>"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()

	h.handleNonStream(rec, resp, "cid-dsm-tool", "deepseek-v4-flash", "prompt", false, false, []string{"Bash"}, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for DSM alias tool calls, got %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSONBody(t, rec.Body.String())
	choices, _ := out["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	if got := asString(choice["finish_reason"]); got != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, got %#v", choice["finish_reason"])
	}
	message, _ := choice["message"].(map[string]any)
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("expected one DSM alias tool call, got %#v", message["tool_calls"])
	}
	if content, exists := message["content"]; !exists || content != nil {
		t.Fatalf("expected raw DSM content hidden when promoted, got %#v", message["content"])
	}
}

type malformedToolRetryDSStub struct {
	payloads []map[string]any
}

func (m *malformedToolRetryDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-id", nil
}

func (m *malformedToolRetryDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow-retry", nil
}

func (m *malformedToolRetryDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id"}, nil
}

func (m *malformedToolRetryDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	m.payloads = append(m.payloads, payload)
	return makeSSEHTTPResponse(
		`data: {"p":"response/content","v":"final answer after retry"}`,
		`data: [DONE]`,
	), nil
}

func (m *malformedToolRetryDSStub) DeleteSessionForToken(_ context.Context, _ string, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (m *malformedToolRetryDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func TestHandleNonStreamRetriesMalformedEmptyReadToolCallWithoutClientLeak(t *testing.T) {
	ds := &malformedToolRetryDSStub{}
	h := &Handler{DS: ds}
	malformed := `<⌜DSML⌝tool_calls><⌜DSML⌝invoke name="Read"><⌜DSML⌝parameter name="file_path"><⌜/DSML⌝parameter><⌜/DSML⌝invoke><⌜/DSML⌝tool_calls>`
	payload, _ := json.Marshal(map[string]any{"p": "response/content", "v": malformed})
	resp := makeSSEHTTPResponse("data: "+string(payload), `data: [DONE]`)
	rec := httptest.NewRecorder()

	h.handleNonStreamWithRetry(rec, context.Background(), &auth.RequestAuth{}, resp, map[string]any{"prompt": "original prompt"}, "pow", "cid-malformed", "deepseek-v4-flash", "original prompt", false, false, []string{"Read"}, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected hidden retry to recover with 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.payloads) != 1 {
		t.Fatalf("expected one hidden retry payload, got %d", len(ds.payloads))
	}
	retryPrompt, _ := ds.payloads[0]["prompt"].(string)
	if !strings.Contains(retryPrompt, "file_path") || !strings.Contains(retryPrompt, malformed) {
		t.Fatalf("expected retry prompt to include malformed tool feedback, got %q", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "tool instructions in the current request") {
		t.Fatalf("expected retry prompt to reference current request tool instructions, got %q", retryPrompt)
	}
	if strings.Contains(rec.Body.String(), "DSML") || strings.Contains(rec.Body.String(), "file_path") {
		t.Fatalf("malformed tool feedback leaked to client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "final answer after retry") {
		t.Fatalf("expected final retry output, got %s", rec.Body.String())
	}
}

func TestHandleNonStreamRetriesToolEmptyOutputWithToolPromptGuidance(t *testing.T) {
	ds := &malformedToolRetryDSStub{}
	h := &Handler{DS: ds}
	resp := makeSSEHTTPResponse(`data: [DONE]`)
	rec := httptest.NewRecorder()

	h.handleNonStreamWithRetry(rec, context.Background(), &auth.RequestAuth{}, resp, map[string]any{"prompt": "original prompt"}, "pow", "cid-tool-empty", "deepseek-v4-flash", "original prompt", false, false, []string{"Read"}, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected hidden retry to recover with 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.payloads) != 1 {
		t.Fatalf("expected one hidden retry payload, got %d", len(ds.payloads))
	}
	retryPrompt, _ := ds.payloads[0]["prompt"].(string)
	for _, want := range []string{"tool instructions in the current request", "ended without natural-language content or a valid tool call", "Do not return an empty response"} {
		if !strings.Contains(retryPrompt, want) {
			t.Fatalf("expected retry prompt to contain %q, got %q", want, retryPrompt)
		}
	}
	if !strings.Contains(rec.Body.String(), "final answer after retry") {
		t.Fatalf("expected final retry output, got %s", rec.Body.String())
	}
}

func TestHandleNonStreamRetriesUnparseableReadFilePathWithoutClientLeak(t *testing.T) {
	ds := &malformedToolRetryDSStub{}
	h := &Handler{DS: ds}
	malformed := `<⌜DSML⌝tool_calls><⌜DSML⌝invoke name="Read"><⌜DSML⌝parameter name="file_path">C:\Users\me\repo\README.md`
	payload, _ := json.Marshal(map[string]any{"p": "response/content", "v": malformed})
	resp := makeSSEHTTPResponse("data: "+string(payload), `data: [DONE]`)
	rec := httptest.NewRecorder()

	h.handleNonStreamWithRetry(rec, context.Background(), &auth.RequestAuth{}, resp, map[string]any{"prompt": "original prompt"}, "pow", "cid-malformed-unparseable", "deepseek-v4-flash", "original prompt", false, false, []string{"Read"}, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected hidden retry to recover with 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.payloads) != 1 {
		t.Fatalf("expected one hidden retry payload, got %d", len(ds.payloads))
	}
	retryPrompt, _ := ds.payloads[0]["prompt"].(string)
	if !strings.Contains(retryPrompt, "file_path") || !strings.Contains(retryPrompt, malformed) {
		t.Fatalf("expected retry prompt to include unparseable tool feedback, got %q", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "tool instructions in the current request") {
		t.Fatalf("expected retry prompt to reference current request tool instructions, got %q", retryPrompt)
	}
	if strings.Contains(rec.Body.String(), "DSML") || strings.Contains(rec.Body.String(), "file_path") || strings.Contains(rec.Body.String(), "README.md") {
		t.Fatalf("unparseable tool feedback leaked to client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "final answer after retry") {
		t.Fatalf("expected final retry output, got %s", rec.Body.String())
	}
}

func TestHandleNonStreamRetriesDSMDLTypoWithOfficialDSMLGuidance(t *testing.T) {
	ds := &malformedToolRetryDSStub{}
	h := &Handler{DS: ds}
	malformed := `<DSMDLtool_calls><DSMDLinvoke name="Read"><DSMDLparameter name="file_path">README.md</DSMDLparameter></DSMDLinvoke></DSMDLtool_calls>`
	payload, _ := json.Marshal(map[string]any{"p": "response/content", "v": malformed})
	resp := makeSSEHTTPResponse("data: "+string(payload), `data: [DONE]`)
	rec := httptest.NewRecorder()

	h.handleNonStreamWithRetry(rec, context.Background(), &auth.RequestAuth{}, resp, map[string]any{"prompt": "original prompt"}, "pow", "cid-nonstream-dsmdl", "deepseek-v4-flash", "original prompt", false, false, []string{"Read"}, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected hidden retry to recover with 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.payloads) != 1 {
		t.Fatalf("expected one hidden retry payload, got %d", len(ds.payloads))
	}
	retryPrompt, _ := ds.payloads[0]["prompt"].(string)
	if !strings.Contains(retryPrompt, malformed) {
		t.Fatalf("expected retry prompt to include malformed DSMDL feedback, got %q", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "<｜DSML｜tool_calls>") || strings.Contains(retryPrompt, "<|DSML|tool_calls>") {
		t.Fatalf("expected retry prompt to use official fullwidth DSML guidance, got %q", retryPrompt)
	}
	if strings.Contains(rec.Body.String(), "DSMDL") || strings.Contains(rec.Body.String(), "README.md") {
		t.Fatalf("DSMDL tool feedback leaked to client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "final answer after retry") {
		t.Fatalf("expected final retry output, got %s", rec.Body.String())
	}
}

func TestHandleStreamRetriesUnparseableReadFilePathWithToolPromptFeedback(t *testing.T) {
	ds := &malformedToolRetryDSStub{}
	h := &Handler{DS: ds}
	malformed := `<⌜DSML⌝tool_calls><⌜DSML⌝invoke name="Read"><⌜DSML⌝parameter name="file_path">C:\Users\me\repo\README.md`
	payload, _ := json.Marshal(map[string]any{"p": "response/content", "v": malformed})
	resp := makeSSEHTTPResponse("data: "+string(payload), `data: [DONE]`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStreamWithRetry(rec, req, &auth.RequestAuth{}, resp, map[string]any{"prompt": "original prompt"}, "pow", "cid-stream-malformed", "deepseek-v4-flash", "original prompt", false, false, []string{"Read"}, nil, nil)
	if len(ds.payloads) != 1 {
		t.Fatalf("expected one hidden stream retry payload, got %d body=%s", len(ds.payloads), rec.Body.String())
	}
	retryPrompt, _ := ds.payloads[0]["prompt"].(string)
	if !strings.Contains(retryPrompt, "tool instructions in the current request") || !strings.Contains(retryPrompt, malformed) {
		t.Fatalf("expected stream retry prompt to include current request tool instructions and malformed feedback, got %q", retryPrompt)
	}
	if strings.Contains(rec.Body.String(), "DSML") || strings.Contains(rec.Body.String(), "file_path") || strings.Contains(rec.Body.String(), "README.md") {
		t.Fatalf("unparseable stream tool feedback leaked to client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "final answer after retry") {
		t.Fatalf("expected final stream retry output, got %s", rec.Body.String())
	}
}

func TestHandleStreamRetriesHashDSMLReadFilePathWithToolPromptFeedback(t *testing.T) {
	ds := &malformedToolRetryDSStub{}
	h := &Handler{DS: ds}
	malformed := `<#DSML#tool_calls>
<#DSML#invoke name="Read">
<#DSML#parameter name="file_path">#CDATA#C:\Users\me\repo\settings.rs#CDATA#</#DSML#parameter>
</#DSML#invoke>
</#DSML#tool_calls>`
	payload, _ := json.Marshal(map[string]any{"p": "response/content", "v": malformed})
	resp := makeSSEHTTPResponse("data: "+string(payload), `data: [DONE]`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStreamWithRetry(rec, req, &auth.RequestAuth{}, resp, map[string]any{"prompt": "original prompt"}, "pow", "cid-stream-hash-dsml", "deepseek-v4-flash", "original prompt", false, false, []string{"Read"}, nil, nil)
	if len(ds.payloads) != 1 {
		t.Fatalf("expected one hidden stream retry payload, got %d body=%s", len(ds.payloads), rec.Body.String())
	}
	retryPrompt, _ := ds.payloads[0]["prompt"].(string)
	if !strings.Contains(retryPrompt, "tool instructions in the current request") || !strings.Contains(retryPrompt, malformed) {
		t.Fatalf("expected stream retry prompt to include current request tool instructions and hash DSML feedback, got %q", retryPrompt)
	}
	if strings.Contains(rec.Body.String(), "#DSML#") || strings.Contains(rec.Body.String(), "settings.rs") {
		t.Fatalf("hash DSML tool feedback leaked to client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "final answer after retry") {
		t.Fatalf("expected final stream retry output, got %s", rec.Body.String())
	}
}

func TestHandleStreamRetriesDSMDLTypoWithOfficialDSMLGuidance(t *testing.T) {
	ds := &malformedToolRetryDSStub{}
	h := &Handler{DS: ds}
	malformed := `<DSMDLtool_calls><DSMDLinvoke name="Read"><DSMDLparameter name="file_path">README.md</DSMDLparameter></DSMDLinvoke></DSMDLtool_calls>`
	payload, _ := json.Marshal(map[string]any{"p": "response/content", "v": malformed})
	resp := makeSSEHTTPResponse("data: "+string(payload), `data: [DONE]`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStreamWithRetry(rec, req, &auth.RequestAuth{}, resp, map[string]any{"prompt": "original prompt"}, "pow", "cid-stream-dsmdl", "deepseek-v4-flash", "original prompt", false, false, []string{"Read"}, nil, nil)
	if len(ds.payloads) != 1 {
		t.Fatalf("expected one hidden stream retry payload, got %d body=%s", len(ds.payloads), rec.Body.String())
	}
	retryPrompt, _ := ds.payloads[0]["prompt"].(string)
	if !strings.Contains(retryPrompt, malformed) {
		t.Fatalf("expected stream retry prompt to include malformed DSMDL feedback, got %q", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "<｜DSML｜tool_calls>") || strings.Contains(retryPrompt, "<|DSML|tool_calls>") {
		t.Fatalf("expected retry prompt to use official fullwidth DSML guidance, got %q", retryPrompt)
	}
	if strings.Contains(rec.Body.String(), "DSMDL") || strings.Contains(rec.Body.String(), "README.md") {
		t.Fatalf("DSMDL tool feedback leaked to client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "final answer after retry") {
		t.Fatalf("expected final stream retry output, got %s", rec.Body.String())
	}
}

func TestHandleStreamRetriesToolEmptyOutputWithToolPromptGuidance(t *testing.T) {
	ds := &malformedToolRetryDSStub{}
	h := &Handler{DS: ds}
	resp := makeSSEHTTPResponse(`data: [DONE]`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStreamWithRetry(rec, req, &auth.RequestAuth{}, resp, map[string]any{"prompt": "original prompt"}, "pow", "cid-stream-tool-empty", "deepseek-v4-flash", "original prompt", false, false, []string{"Read"}, nil, nil)
	if len(ds.payloads) != 1 {
		t.Fatalf("expected one hidden stream retry payload, got %d body=%s", len(ds.payloads), rec.Body.String())
	}
	retryPrompt, _ := ds.payloads[0]["prompt"].(string)
	for _, want := range []string{"tool instructions in the current request", "ended without natural-language content or a valid tool call", "Do not return an empty response"} {
		if !strings.Contains(retryPrompt, want) {
			t.Fatalf("expected stream retry prompt to contain %q, got %q", want, retryPrompt)
		}
	}
	if !strings.Contains(rec.Body.String(), "final answer after retry") {
		t.Fatalf("expected final stream retry output, got %s", rec.Body.String())
	}
}

func TestHandleStreamSuppressesTextAfterToolCallInSameTurn(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/content","v":"<tool_calls><invoke name=\"Read\"><parameter name=\"file_path\">/tmp/input.txt</parameter></invoke></tool_calls>\n继续读取剩余文件。"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStream(rec, req, resp, "cid-tool-post-text", "deepseek-v4-flash", "prompt", false, false, []string{"Read"}, nil, nil)
	frames, _ := parseSSEDataFrames(t, rec.Body.String())
	if !streamHasToolCallsDelta(frames) {
		t.Fatalf("expected tool call delta, body=%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "继续读取剩余文件") {
		t.Fatalf("post-tool text leaked after tool call boundary: %s", rec.Body.String())
	}
}

func TestHandleStreamToolsPlainTextStreamsBeforeFinish(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/content","v":"你好，"}`,
		`data: {"p":"response/content","v":"这是普通文本回复。"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStream(rec, req, resp, "cid6", "deepseek-v4-flash", "prompt", false, false, []string{"search"}, nil, nil)

	frames, done := parseSSEDataFrames(t, rec.Body.String())
	if !done {
		t.Fatalf("expected [DONE], body=%s", rec.Body.String())
	}
	if streamHasToolCallsDelta(frames) {
		t.Fatalf("did not expect tool_calls delta for plain text: %s", rec.Body.String())
	}
	content := strings.Builder{}
	for _, frame := range frames {
		choices, _ := frame["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if c, ok := delta["content"].(string); ok {
				content.WriteString(c)
			}
		}
	}
	if got := content.String(); got == "" {
		t.Fatalf("expected streamed content in tool mode plain text, body=%s", rec.Body.String())
	}
	if streamFinishReason(frames) != "stop" {
		t.Fatalf("expected finish_reason=stop, body=%s", rec.Body.String())
	}
}

func TestHandleStreamIncompleteCapturedToolJSONFlushesAsTextOnFinalize(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/content","v":"{\"tool_calls\":[{\"name\":\"search\""}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStream(rec, req, resp, "cid10", "deepseek-v4-flash", "prompt", false, false, []string{"search"}, nil, nil)

	frames, done := parseSSEDataFrames(t, rec.Body.String())
	if !done {
		t.Fatalf("expected [DONE], body=%s", rec.Body.String())
	}
	if streamHasToolCallsDelta(frames) {
		t.Fatalf("did not expect tool_calls delta for incomplete json, body=%s", rec.Body.String())
	}
	content := strings.Builder{}
	for _, frame := range frames {
		choices, _ := frame["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if c, ok := delta["content"].(string); ok {
				content.WriteString(c)
			}
		}
	}
	if !strings.Contains(strings.ToLower(content.String()), "tool_calls") || !strings.Contains(content.String(), "{") {
		t.Fatalf("expected incomplete capture to flush as plain text instead of stalling, got=%q", content.String())
	}
}

func TestHandleStreamPromotesThinkingToolCallsOnFinalizeWithoutMidstreamIntercept(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/thinking_content","v":"<tool_calls><invoke name=\"search\"><parameter name=\"q\">from-thinking</parameter></invoke></tool_calls>"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStream(rec, req, resp, "cid-thinking-stream", "deepseek-v4-pro", "prompt", true, false, []string{"search"}, nil, nil)

	frames, done := parseSSEDataFrames(t, rec.Body.String())
	if !done {
		t.Fatalf("expected [DONE], body=%s", rec.Body.String())
	}
	if !streamHasToolCallsDelta(frames) {
		t.Fatalf("expected tool_calls delta from finalize fallback, body=%s", rec.Body.String())
	}
	reasoningSeen := false
	for _, frame := range frames {
		choices, _ := frame["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if asString(delta["reasoning_content"]) != "" {
				reasoningSeen = true
			}
		}
	}
	if !reasoningSeen {
		t.Fatalf("expected reasoning_content to stream before finalize fallback, body=%s", rec.Body.String())
	}
	if streamFinishReason(frames) != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, body=%s", rec.Body.String())
	}
}

func TestHandleStreamPromotesHiddenThinkingDSMLToolCallsOnFinalize(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/thinking_content","v":"<|DSML|tool_calls><|DSML|invoke name=\"search\"><|DSML|parameter name=\"q\">from-hidden-thinking</|DSML|parameter></|DSML|invoke></|DSML|tool_calls>"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStream(rec, req, resp, "cid-hidden-thinking-stream", "deepseek-v4-pro", "prompt", false, false, []string{"search"}, nil, nil)

	frames, done := parseSSEDataFrames(t, rec.Body.String())
	if !done {
		t.Fatalf("expected [DONE], body=%s", rec.Body.String())
	}
	if !streamHasToolCallsDelta(frames) {
		t.Fatalf("expected tool_calls delta from hidden thinking fallback, body=%s", rec.Body.String())
	}
	for _, frame := range frames {
		choices, _ := frame["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if asString(delta["reasoning_content"]) != "" {
				t.Fatalf("did not expect hidden reasoning_content delta, body=%s", rec.Body.String())
			}
		}
	}
	if streamFinishReason(frames) != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, body=%s", rec.Body.String())
	}
}

func TestHandleStreamEmitsDistinctToolCallIDsAcrossSeparateToolBlocks(t *testing.T) {
	h := &Handler{}
	resp := makeSSEHTTPResponse(
		`data: {"p":"response/content","v":"前置文本\n<tool_calls>\n  <invoke name=\"read_file\">\n    <parameter name=\"path\">README.MD</parameter>\n  </invoke>\n</tool_calls>"}`,
		`data: {"p":"response/content","v":"中间文本\n<tool_calls>\n  <invoke name=\"search\">\n    <parameter name=\"q\">golang</parameter>\n  </invoke>\n</tool_calls>"}`,
		`data: [DONE]`,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	h.handleStream(rec, req, resp, "cid-multi", "deepseek-v4-flash", "prompt", false, false, []string{"read_file", "search"}, nil, nil)

	frames, done := parseSSEDataFrames(t, rec.Body.String())
	if !done {
		t.Fatalf("expected [DONE], body=%s", rec.Body.String())
	}

	ids := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, frame := range frames {
		choices, _ := frame["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			toolCalls, _ := delta["tool_calls"].([]any)
			for _, rawCall := range toolCalls {
				call, _ := rawCall.(map[string]any)
				id := asString(call["id"])
				if id == "" {
					continue
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}

	if len(ids) != 2 {
		t.Fatalf("expected two distinct tool call ids, got %#v body=%s", ids, rec.Body.String())
	}
	if ids[0] == ids[1] {
		t.Fatalf("expected distinct tool call ids across blocks, got %#v body=%s", ids, rec.Body.String())
	}
}
