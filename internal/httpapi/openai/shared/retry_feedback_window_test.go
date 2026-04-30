package shared

import (
	"strings"
	"testing"
)

func TestPushRetryFeedbackWindowKeepsOnlyLatestTwo(t *testing.T) {
	history := []RetryFeedback{
		{Attempt: 1, Summary: "first"},
		{Attempt: 2, Summary: "second"},
	}
	history = PushRetryFeedbackWindow(history, RetryFeedback{Attempt: 3, Summary: "third"})
	if len(history) != 2 {
		t.Fatalf("expected 2 retry feedback entries, got %d", len(history))
	}
	if history[0].Attempt != 2 || history[1].Attempt != 3 {
		t.Fatalf("expected attempts [2 3], got [%d %d]", history[0].Attempt, history[1].Attempt)
	}
}

func TestPushRetryFeedbackWindowDropsOldEntriesCleanly(t *testing.T) {
	old := strings.Repeat("x", 300)
	mid := strings.Repeat("y", 300)
	history := []RetryFeedback{
		{Attempt: 1, Summary: "old", Raw: old},
		{Attempt: 2, Summary: "mid", Raw: mid},
	}
	window := PushRetryFeedbackWindow(history, RetryFeedback{Attempt: 3, Summary: "new"})
	if len(window) != 2 {
		t.Fatalf("expected 2 retry feedback entries, got %d", len(window))
	}
	if window[0].Attempt != 2 || window[1].Attempt != 3 {
		t.Fatalf("expected attempts [2 3], got [%d %d]", window[0].Attempt, window[1].Attempt)
	}
	for _, item := range window {
		if strings.Contains(item.Raw, old) {
			t.Fatalf("expected oldest raw payload to be dropped completely, got %#v", window)
		}
	}
}

func TestRenderRetryFeedbackWindowFormatsLatestFailures(t *testing.T) {
	text := RenderRetryFeedbackWindow([]RetryFeedback{
		{Attempt: 4, Summary: "invalid tool-call structure"},
		{Attempt: 5, Summary: "empty output", Raw: "partial bad output"},
	})
	for _, want := range []string{
		"Recent retry failures:",
		"- Retry #4: invalid tool-call structure",
		"- Retry #5: empty output",
		"partial bad output",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected rendered window to contain %q, got %q", want, text)
		}
	}
}
