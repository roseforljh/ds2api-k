package shared

import (
	"testing"
	"time"
)

func TestEmptyOutputRetryWindowDurationIsTenMinutes(t *testing.T) {
	if got := EmptyOutputRetryWindow(); got != 10*time.Minute {
		t.Fatalf("expected retry window to be 10 minutes, got %s", got)
	}
}

func TestEmptyOutputRetryWithinWindow(t *testing.T) {
	startedAt := time.Now().Add(-9*time.Minute - 30*time.Second)
	if !EmptyOutputRetryWithinWindow(startedAt, time.Now()) {
		t.Fatal("expected retry to remain allowed inside 10 minute window")
	}
}

func TestEmptyOutputRetryOutsideWindow(t *testing.T) {
	startedAt := time.Now().Add(-10*time.Minute - time.Second)
	if EmptyOutputRetryWithinWindow(startedAt, time.Now()) {
		t.Fatal("expected retry to stop after 10 minute window")
	}
}
