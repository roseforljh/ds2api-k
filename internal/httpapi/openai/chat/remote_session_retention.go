package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/config"
)

type remoteSessionState string

const (
	remoteSessionActive  remoteSessionState = "active"
	remoteSessionSuccess remoteSessionState = "success"
	remoteSessionFailed  remoteSessionState = "failed"
	remoteSessionStopped remoteSessionState = "stopped"
)

type remoteSessionTerminalState string

const (
	remoteTerminalSuccess remoteSessionTerminalState = "success"
	remoteTerminalFailed  remoteSessionTerminalState = "failed"
	remoteTerminalStopped remoteSessionTerminalState = "stopped"
)

type remoteSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]remoteSessionRecord
}

type remoteSessionRecord struct {
	TokenHash string
	AccountID string
	SessionID string
	State     remoteSessionState
	CreatedAt time.Time
	UpdatedAt time.Time
}

func newRemoteSessionRegistry() *remoteSessionRegistry {
	return &remoteSessionRegistry{sessions: map[string]remoteSessionRecord{}}
}

func (h *Handler) remoteSessionRegistry() *remoteSessionRegistry {
	if h == nil {
		return nil
	}
	h.remoteSessionsOnce.Do(func() {
		h.remoteSessions = newRemoteSessionRegistry()
	})
	return h.remoteSessions
}

func remoteSessionKey(tokenHash, sessionID string) string {
	return tokenHash + "|" + strings.TrimSpace(sessionID)
}

func hashRemoteSessionToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func (r *remoteSessionRegistry) register(token, accountID, sessionID string) {
	if r == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	now := time.Now()
	tokenHash := hashRemoteSessionToken(token)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[remoteSessionKey(tokenHash, sessionID)] = remoteSessionRecord{
		TokenHash: tokenHash,
		AccountID: strings.TrimSpace(accountID),
		SessionID: strings.TrimSpace(sessionID),
		State:     remoteSessionActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (r *remoteSessionRegistry) mark(token, sessionID string, state remoteSessionState) {
	if r == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	tokenHash := hashRemoteSessionToken(token)
	key := remoteSessionKey(tokenHash, sessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[key]
	if !ok {
		return
	}
	record.State = state
	record.UpdatedAt = time.Now()
	r.sessions[key] = record
}

func (r *remoteSessionRegistry) successCandidates(token string) []remoteSessionRecord {
	if r == nil || strings.TrimSpace(token) == "" {
		return nil
	}
	tokenHash := hashRemoteSessionToken(token)
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []remoteSessionRecord{}
	for _, record := range r.sessions {
		if record.TokenHash == tokenHash && record.State == remoteSessionSuccess {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (r *remoteSessionRegistry) successRecordOlderThan(token, sessionID string, age time.Duration) bool {
	if r == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(sessionID) == "" {
		return false
	}
	tokenHash := hashRemoteSessionToken(token)
	key := remoteSessionKey(tokenHash, sessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.sessions[key]
	if !ok || record.State != remoteSessionSuccess {
		return false
	}
	return time.Since(record.UpdatedAt) >= age
}

func (r *remoteSessionRegistry) remove(token, sessionID string) {
	if r == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	tokenHash := hashRemoteSessionToken(token)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, remoteSessionKey(tokenHash, sessionID))
}

func (h *Handler) registerRemoteSession(a *auth.RequestAuth, sessionID string) {
	if a == nil {
		return
	}
	mode := strings.TrimSpace(strings.ToLower(h.Store.AutoDeleteMode()))
	if mode != "retention" {
		return
	}
	h.remoteSessionRegistry().register(a.DeepSeekToken, a.AccountID, sessionID)
}

func (h *Handler) markRemoteSessionTerminal(ctx context.Context, a *auth.RequestAuth, sessionID string, terminal remoteSessionTerminalState) {
	if h == nil || a == nil || strings.TrimSpace(a.DeepSeekToken) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	mode := strings.TrimSpace(strings.ToLower(h.Store.AutoDeleteMode()))
	if mode != "retention" {
		return
	}
	registry := h.remoteSessionRegistry()
	switch terminal {
	case remoteTerminalSuccess:
		registry.mark(a.DeepSeekToken, sessionID, remoteSessionSuccess)
		h.pruneSuccessfulRemoteSessions(ctx, a)
		h.scheduleRemoteSessionRetentionDeletion(a, sessionID)
	case remoteTerminalStopped:
		registry.remove(a.DeepSeekToken, sessionID)
	default:
		registry.remove(a.DeepSeekToken, sessionID)
	}
}

func remoteSessionRetentionLimit(store any) int {
	if s, ok := store.(accountInflightLimitReader); ok {
		if n := s.RuntimeAccountMaxInflight(); n > 0 {
			if n > 32 {
				return 32
			}
			return n
		}
	}
	return 2
}

func (h *Handler) pruneSuccessfulRemoteSessions(ctx context.Context, a *auth.RequestAuth) {
	if h == nil || a == nil || strings.TrimSpace(a.DeepSeekToken) == "" {
		return
	}
	candidates := h.remoteSessionRegistry().successCandidates(a.DeepSeekToken)
	if len(candidates) == 0 {
		return
	}
	limit := remoteSessionRetentionLimit(h.Store)
	now := time.Now()
	for i, record := range candidates {
		if i < limit && now.Sub(record.UpdatedAt) < remoteSessionRetentionDelay {
			continue
		}
		deleteBaseCtx := context.WithoutCancel(ctx)
		deleteCtx, cancel := context.WithTimeout(deleteBaseCtx, 10*time.Second)
		_, err := h.DS.DeleteSessionForToken(deleteCtx, a.DeepSeekToken, record.SessionID)
		cancel()
		if err != nil {
			config.Logger.Warn("[remote_session_retention] success prune failed", "account", a.AccountID, "session_id", record.SessionID, "error", err)
			continue
		}
		h.remoteSessionRegistry().remove(a.DeepSeekToken, record.SessionID)
		config.Logger.Debug("[remote_session_retention] success pruned", "account", a.AccountID, "session_id", record.SessionID)
	}
}
