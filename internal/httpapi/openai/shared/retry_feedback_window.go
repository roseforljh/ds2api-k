package shared

import (
	"fmt"
	"strings"
)

type RetryFeedback struct {
	Attempt int
	Kind    string
	Summary string
	Raw     string
}

func PushRetryFeedbackWindow(history []RetryFeedback, next RetryFeedback) []RetryFeedback {
	keep := history
	if len(keep) > 1 {
		keep = keep[len(keep)-1:]
	}
	window := make([]RetryFeedback, 0, 2)
	for _, item := range keep {
		window = append(window, sanitizeRetryFeedback(item))
	}
	window = append(window, sanitizeRetryFeedback(next))
	if len(window) > 2 {
		window = window[len(window)-2:]
	}
	return window
}

func RenderRetryFeedbackWindow(history []RetryFeedback) string {
	if len(history) == 0 {
		return ""
	}
	lines := []string{
		"Recent retry failures:",
	}
	for _, item := range history {
		summary := strings.TrimSpace(item.Summary)
		if summary == "" {
			summary = "unknown retry failure"
		}
		lines = append(lines, fmt.Sprintf("- Retry #%d: %s", item.Attempt, summary))
		if raw := strings.TrimSpace(item.Raw); raw != "" {
			lines = append(lines, raw)
		}
	}
	return strings.Join(lines, "\n")
}

func sanitizeRetryFeedback(next RetryFeedback) RetryFeedback {
	next.Kind = strings.TrimSpace(next.Kind)
	next.Summary = strings.TrimSpace(next.Summary)
	raw := strings.TrimSpace(next.Raw)
	if len(raw) > 400 {
		raw = raw[:400]
	}
	next.Raw = raw
	return next
}
