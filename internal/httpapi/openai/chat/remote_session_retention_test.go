package chat

import (
	"context"
	"testing"
	"time"

	"ds2api/internal/auth"
)

func setRemoteSessionUpdatedAt(t *testing.T, registry *remoteSessionRegistry, token, sessionID string, updatedAt time.Time) {
	t.Helper()
	tokenHash := hashRemoteSessionToken(token)
	key := remoteSessionKey(tokenHash, sessionID)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	record, ok := registry.sessions[key]
	if !ok {
		t.Fatalf("session %s not found in registry", sessionID)
	}
	record.UpdatedAt = updatedAt
	record.CreatedAt = updatedAt
	registry.sessions[key] = record
}

func TestPruneSuccessfulRemoteSessionsDeletesOnlyOldSuccess(t *testing.T) {
	ds := &autoDeleteModeDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{autoDeleteMode: "retention", accountMaxInflight: 2},
		DS:    ds,
	}
	a := &auth.RequestAuth{DeepSeekToken: "token", AccountID: "acct"}
	registry := h.remoteSessionRegistry()
	now := time.Now()

	for i, sessionID := range []string{"success-old", "success-mid", "success-new"} {
		registry.register(a.DeepSeekToken, a.AccountID, sessionID)
		registry.mark(a.DeepSeekToken, sessionID, remoteSessionSuccess)
		setRemoteSessionUpdatedAt(t, registry, a.DeepSeekToken, sessionID, now.Add(time.Duration(i)*time.Second))
	}
	registry.register(a.DeepSeekToken, a.AccountID, "active-old")
	setRemoteSessionUpdatedAt(t, registry, a.DeepSeekToken, "active-old", now.Add(-time.Hour))
	registry.register(a.DeepSeekToken, a.AccountID, "failed-old")
	registry.mark(a.DeepSeekToken, "failed-old", remoteSessionFailed)
	setRemoteSessionUpdatedAt(t, registry, a.DeepSeekToken, "failed-old", now.Add(-time.Hour))

	h.pruneSuccessfulRemoteSessions(context.Background(), a)

	if ds.singleCalls != 1 {
		t.Fatalf("delete calls=%d want=1", ds.singleCalls)
	}
	if ds.lastSessionID != "success-old" {
		t.Fatalf("deleted session=%q want success-old", ds.lastSessionID)
	}
	for _, sessionID := range []string{"success-mid", "success-new", "active-old", "failed-old"} {
		key := remoteSessionKey(hashRemoteSessionToken(a.DeepSeekToken), sessionID)
		registry.mu.Lock()
		_, ok := registry.sessions[key]
		registry.mu.Unlock()
		if !ok {
			t.Fatalf("expected %s to remain", sessionID)
		}
	}
}

func TestPruneSuccessfulRemoteSessionsDeletesOnlyStaleSuccess(t *testing.T) {
	ds := &autoDeleteModeDSStub{}
	h := &Handler{
		Store: mockOpenAIConfig{autoDeleteMode: "retention", accountMaxInflight: 32},
		DS:    ds,
	}
	a := &auth.RequestAuth{DeepSeekToken: "token", AccountID: "acct"}
	registry := h.remoteSessionRegistry()
	stale := time.Now().Add(-remoteSessionRetentionDelay - time.Minute)

	registry.register(a.DeepSeekToken, a.AccountID, "success-stale")
	registry.mark(a.DeepSeekToken, "success-stale", remoteSessionSuccess)
	setRemoteSessionUpdatedAt(t, registry, a.DeepSeekToken, "success-stale", stale)
	registry.register(a.DeepSeekToken, a.AccountID, "active-stale")
	setRemoteSessionUpdatedAt(t, registry, a.DeepSeekToken, "active-stale", stale)

	h.pruneSuccessfulRemoteSessions(context.Background(), a)

	if ds.singleCalls != 1 {
		t.Fatalf("delete calls=%d want=1", ds.singleCalls)
	}
	if ds.lastSessionID != "success-stale" {
		t.Fatalf("deleted session=%q want success-stale", ds.lastSessionID)
	}
	key := remoteSessionKey(hashRemoteSessionToken(a.DeepSeekToken), "active-stale")
	registry.mu.Lock()
	_, activeOK := registry.sessions[key]
	registry.mu.Unlock()
	if !activeOK {
		t.Fatalf("expected stale active session to remain")
	}
}

func TestRegisterRemoteSessionSkipsNonRetentionModes(t *testing.T) {
	h := &Handler{
		Store: mockOpenAIConfig{autoDeleteMode: "none"},
	}
	a := &auth.RequestAuth{DeepSeekToken: "token", AccountID: "acct"}

	h.registerRemoteSession(a, "session-id")

	registry := h.remoteSessionRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.sessions) != 0 {
		t.Fatalf("expected non-retention mode not to register remote sessions, got %#v", registry.sessions)
	}
}

func TestMarkRemoteSessionTerminalRemovesFailedAndStoppedRecords(t *testing.T) {
	tests := []struct {
		name     string
		terminal remoteSessionTerminalState
	}{
		{name: "failed", terminal: remoteTerminalFailed},
		{name: "stopped", terminal: remoteTerminalStopped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{
				Store: mockOpenAIConfig{autoDeleteMode: "retention"},
				DS:    &autoDeleteModeDSStub{},
			}
			a := &auth.RequestAuth{DeepSeekToken: "token", AccountID: "acct"}

			h.registerRemoteSession(a, "session-id")
			h.markRemoteSessionTerminal(context.Background(), a, "session-id", tc.terminal)

			registry := h.remoteSessionRegistry()
			registry.mu.Lock()
			defer registry.mu.Unlock()
			if len(registry.sessions) != 0 {
				t.Fatalf("expected %s terminal state to remove registry record, got %#v", tc.name, registry.sessions)
			}
		})
	}
}
