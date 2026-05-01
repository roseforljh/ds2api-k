package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ds2api/internal/httpapi/openai/shared"
)

func TestAppendToolEmptyOutputRetrySuffixIncludesSkeletonOnly(t *testing.T) {
	got := appendToolEmptyOutputRetrySuffix("BASE PROMPT", nil)
	for _, want := range []string{
		"Your previous reply was invalid or empty and was not shown to the user.",
		"A) Normal answer: plain natural-language answer only.",
		"B) Tool call skeleton:",
		"<｜DSML｜tool_calls>",
		"<｜DSML｜invoke name=\"VALID_TOOL_NAME_FROM_CURRENT_TOOL_LIST\">",
		"<｜DSML｜parameter name=\"VALID_PARAMETER_NAME\" string=\"true\"><![CDATA[NON_EMPTY_VALUE]]></｜DSML｜parameter>",
		"Do not copy placeholder names literally.",
		"Now output only the corrected final answer or one valid tool call.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected retry suffix to contain %q, got %q", want, got)
		}
	}
	for _, forbidden := range []string{"README.md", "file_path", "Read\""} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected empty-output retry suffix not to contain concrete example value %q, got %q", forbidden, got)
		}
	}
}

func TestAppendMalformedToolCallRetrySuffixIncludesSkeletonAndInvalidOutput(t *testing.T) {
	malformed := `<|DSML|invoke name="Read"></|DSML|invoke>`
	got := appendMalformedToolCallRetrySuffix("BASE PROMPT", malformed, nil)
	for _, want := range []string{
		"Your previous reply included an invalid tool call and was not shown to the user.",
		"The server discarded that malformed tool-call text before it reached the user.",
		"Tool-call skeleton:",
		"<｜DSML｜tool_calls>",
		"<｜DSML｜invoke name=\"VALID_TOOL_NAME_FROM_CURRENT_TOOL_LIST\">",
		"<｜DSML｜parameter name=\"VALID_PARAMETER_NAME\" string=\"true\"><![CDATA[NON_EMPTY_VALUE]]></｜DSML｜parameter>",
		"Parameter names must come from that tool's schema in the current request.",
		"Do not reuse malformed tag variants such as DSML double-underscore tags, duplicated leading angle brackets, DSMDL typo tags, ASCII-pipe DSML tags, or bare tool_calls tags.",
		"Every opened <｜DSML｜tool_calls>, <｜DSML｜invoke>, and <｜DSML｜parameter> tag must be closed.",
		"Invalid previous reply:",
		malformed,
		"Now output only one corrected tool call and nothing else.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected malformed retry suffix to contain %q, got %q", want, got)
		}
	}
	for _, forbidden := range []string{"README.md", "file_path"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected malformed retry suffix not to contain concrete example value %q, got %q", forbidden, got)
		}
	}
}

func TestAppendRetrySuffixUsesOfficialFullwidthDSMLSkeleton(t *testing.T) {
	empty := appendToolEmptyOutputRetrySuffix("BASE PROMPT", nil)
	malformed := appendMalformedToolCallRetrySuffix("BASE PROMPT", `<DSMDLtool_calls><DSMDLinvoke name="Read"></DSMDLinvoke></DSMDLtool_calls>`, nil)
	for _, got := range []string{empty, malformed} {
		if strings.Contains(got, "<|DSML|tool_calls>") {
			t.Fatalf("expected retry suffix to avoid ASCII DSML skeleton, got %q", got)
		}
		if !strings.Contains(got, "<｜DSML｜tool_calls>") {
			t.Fatalf("expected retry suffix to use official fullwidth DSML skeleton, got %q", got)
		}
	}
}

func TestAppendRetrySuffixIncludesOnlyLatestTwoRetryFailures(t *testing.T) {
	history := []shared.RetryFeedback{
		{Attempt: 2, Summary: "too old"},
		{Attempt: 3, Summary: "invalid tool-call structure"},
		{Attempt: 4, Summary: "empty output"},
	}
	window := shared.PushRetryFeedbackWindow(history[:2], history[2])
	got := appendToolEmptyOutputRetrySuffix("BASE PROMPT", window)
	if strings.Contains(got, "too old") {
		t.Fatalf("expected oldest retry failure to be dropped, got %q", got)
	}
	for _, want := range []string{"Recent retry failures:", "Retry #3: invalid tool-call structure", "Retry #4: empty output"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected retry window text to contain %q, got %q", want, got)
		}
	}
}

func TestShouldRetryChatNonStreamMalformedToolCallDespiteContentFilter(t *testing.T) {
	result := chatNonStreamResult{
		contentFilter:         true,
		detectedCalls:         0,
		malformedToolFeedback: `<dsml-tool-calls><dsml-invoke name="Read"></dsml-invoke></dsml-tool-calls>`,
	}
	if !shouldRetryChatNonStream(result, 0, time.Now(), time.Now().Add(time.Second)) {
		t.Fatal("expected malformed tool call to retry even when upstream marks content_filter")
	}
}

func TestChatStreamFinalizeDefersMalformedToolCallDespiteContentFilter(t *testing.T) {
	rec := httptest.NewRecorder()
	runtime := newChatStreamRuntime(
		rec,
		http.NewResponseController(rec),
		false,
		"chatcmpl-test",
		1,
		"deepseek-chat",
		"prompt",
		false,
		false,
		false,
		[]string{"Read"},
		nil,
		true,
		false,
	)
	runtime.toolSieve.MalformedToolFeedback = `<dsml-tool-calls><dsml-invoke name="Read"></dsml-invoke></dsml-tool-calls>`

	if runtime.finalize("content_filter", true) {
		t.Fatal("expected malformed tool call to defer terminal write so retry can run")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no stream output before retry, got %q", rec.Body.String())
	}
	if runtime.malformedToolFeedback == "" {
		t.Fatal("expected malformed feedback to be preserved for retry prompt")
	}
}
