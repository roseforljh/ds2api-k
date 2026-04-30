# Success-Only Session Retention Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make session/history retention concurrency-safe by deleting only conversations explicitly marked as successful.

**Architecture:** Replace destructive “delete all previous sessions” retention with success-only pruning. Local debug history keeps all active/non-success terminal entries and only prunes existing successful records (`status == "success"`) beyond the retention target. Remote DeepSeek session pruning is registry-only: the process tracks sessions it created and deletes only records that this process explicitly marked as successful.

**Tech Stack:** Go HTTP handlers, existing `chathistory.Store`, existing DeepSeek session APIs, table-driven Go tests.

---

## Rules Locked by Review

1. Local automatic retention deletion may delete only conversations explicitly marked `status == "success"`; current code does not use `status == "completed"`.
2. Remote automatic retention deletion may delete only registry records explicitly marked `state == "success"`.
3. `streaming`, `running`, `error`, `failed`, `stopped`, `cancelled`, empty, and `unknown` are never automatic deletion candidates.
4. Retention limit controls successful-session candidates only; it must not force-delete active, failed, stopped, or unknown sessions.
5. Default retention target should align with configured per-account concurrency: `RuntimeAccountMaxInflight()`.
6. Remote pruning must not call `DeleteAllSessionsForToken` in `retention` mode.
7. Remote pruning must not require `FetchSessionPage`; not-in-registry remote sessions are `unknown` and protected.
8. Crashes/restarts turn remote session state into `unknown`; unknown remote sessions are not automatically deleted by success-only retention.
9. Final deletion predicate is: `success && (older than newest N successful sessions || older than retention TTL)`.

## Target Behavior

```text
Request starts
  ↓
Create remote session
  ↓
Register token/session as active
  ↓
Completion succeeds?
  ├─ yes → mark remote session success; local history status success
  └─ no  → mark remote session failed/stopped; local history status error/stopped/etc.
  ↓
Retention prune
  ↓
Delete only success candidates older than newest N success sessions, or success candidates past TTL
```

For local debug history:

```text
Stored records = all non-success records + newest N success records
Deletion candidates = success records outside newest N, plus success records past TTL if TTL cleanup is running
```

For remote DeepSeek sessions:

```text
Deletion candidates = sessions created by this process and marked success
Protected = active/running/failed/stopped/cancelled/unknown/not-in-registry sessions
```

---

### Task 1: Add local history success-only pruning tests

**Files:**
- Test: `internal/chathistory/store_test.go`
- Modify later: `internal/chathistory/store.go`

**Step 1: Add test for active records exceeding limit**

Add a test that creates:
- one `success` entry
- one still-`streaming` entry
- a limit of `1`

Expected:
- the `streaming` entry remains
- the newest `success` entry remains
- total count may exceed `1` because active entries are protected

**Step 2: Add test for pruning only old success records**

Create three entries updated to `Status: "success"` with limit `2`.

Expected:
- newest two success entries remain
- oldest success entry is deleted

**Step 3: Add test for error records protected**

Create:
- two success entries
- one `error` entry
- limit `1`

Expected:
- newest one success entry remains
- error entry remains
- older success entry is deleted

**Step 4: Add test for stopped records protected**

Create:
- two success entries
- one `stopped` entry
- limit `1`

Expected:
- newest one success entry remains
- stopped entry remains
- older success entry is deleted

**Step 5: Add TTL tests for success-only expiry**

Create records whose `UpdatedAt`/`CompletedAt` are older than `debugRetentionTTL`:
- stale `success`
- stale `streaming`
- stale `error`
- stale `stopped`

Expected:
- stale `success` is deleted
- stale `streaming`, `error`, and `stopped` remain

This specifically guards `enforceRetentionLocked()`, not just `rebuildIndexLocked()`.

**Step 6: Run targeted tests and verify failure**

Run:

```powershell
go test ./internal/chathistory -run "Retention|Limit|Success|Stopped" -count=1
```

Expected: FAIL until `store.go` is updated.

---

### Task 2: Implement success-only local history retention

**Files:**
- Modify: `internal/chathistory/store.go`
- Test: `internal/chathistory/store_test.go`

**Step 1: Introduce explicit status helper**

Add:

```go
func isPrunableSuccessStatus(status string) bool {
	return strings.TrimSpace(strings.ToLower(status)) == "success"
}
```

Do not treat `CompletedAt != 0` as sufficient.

**Step 2: Replace unconditional limit pruning**

Update `rebuildIndexLocked` so it:
1. builds all summaries
2. sorts by updated/created desc
3. splits records into success and protected
4. deletes only success records beyond `s.state.Limit`
5. keeps every protected record

Pseudo-code:

```go
successful := []SummaryEntry{}
protected := []SummaryEntry{}
for _, item := range summaries {
	if isPrunableSuccessStatus(item.Status) {
		successful = append(successful, item)
	} else {
		protected = append(protected, item)
	}
}

if s.state.Limit != DisabledLimit && len(successful) > s.state.Limit {
	for _, item := range successful[s.state.Limit:] {
		s.markDetailDeletedLocked(item.ID)
		delete(s.details, item.ID)
	}
	successful = successful[:s.state.Limit]
}

s.state.Items = mergeAndSort(protected, successful)
```

**Step 3: Make TTL expiry success-only**

Update the TTL loop in `enforceRetentionLocked()` so it checks status before deleting:

```go
if !isPrunableSuccessStatus(item.Status) {
	continue
}
```

Only stale `success` records may be removed by `debugRetentionTTL`. Stale `streaming`, `error`, `stopped`, empty, and unknown statuses must remain.

**Step 4: Stop forcing limit back to 1 during retention**

Update `enforceRetentionLocked` so it validates the limit but does not hard-reset every value to `DefaultLimit`.

**Step 5: Run tests**

Run:

```powershell
go test ./internal/chathistory -count=1
```

Expected: PASS.

**Step 6: Commit**

```powershell
git add internal/chathistory/store.go internal/chathistory/store_test.go
git commit -m "fix: prune only successful chat history entries"
```

---

### Task 3: Make local history limit configurable from runtime concurrency

**Files:**
- Modify: `internal/chathistory/store.go`
- Modify: code path that constructs or configures `ChatHistory` store if needed
- Test: `internal/chathistory/store_test.go`
- Test: `internal/httpapi/openai/chat/chat_history_test.go`

**Step 1: Keep minimal API**

Prefer not to add a large new config surface. Use existing `SetLimit(limit int)` or constructor initialization to set:

```text
history retention limit = Store.RuntimeAccountMaxInflight()
```

Default remains `2` because `RuntimeAccountMaxInflight()` defaults to `2`.

**Step 1a: Use a chat-local narrow interface for runtime concurrency**

Do **not** add `RuntimeAccountMaxInflight()` to `shared.ConfigReader`; that widens the shared interface and forces unrelated mocks to change.

Instead, in the chat package or nearest setup code, use a narrow optional interface:

```go
type accountInflightLimitReader interface {
	RuntimeAccountMaxInflight() int
}

func chatHistoryRetentionLimit(store shared.ConfigReader) int {
	if s, ok := store.(accountInflightLimitReader); ok {
		if n := s.RuntimeAccountMaxInflight(); n > 0 {
			return n
		}
	}
	return 2
}
```

This keeps the blast radius local to chat/history retention.

**Step 2: Expand allowed limit validation**

Replace hardcoded:

```go
MaxLimit = 1
allowedLimits = map[int]struct{}{1: {}}
```

with a bounded explicit range, for example:

```go
const MaxLimit = 32
```

Validation:

```go
func normalizeLimit(limit int) int {
	if limit == DisabledLimit {
		return DisabledLimit
	}
	if limit < 1 {
		return DefaultLimit
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}
```

**Step 3: Make `SetLimit` honor input**

Change `SetLimit(limit int)` to use `normalizeLimit(limit)`.

Update existing `internal/chathistory/store_test.go` limit-validation expectations:
- `SetLimit(999)` should not error; it should clamp to `MaxLimit`
- `SetLimit(-1)` should normalize to `DefaultLimit`
- `SetLimit(0)` should keep `DisabledLimit` only if disabled history is still a supported public behavior; otherwise explicitly normalize to `DefaultLimit` and update tests accordingly

**Step 4: Run tests**

Run:

```powershell
go test ./internal/chathistory -count=1
go test ./internal/httpapi/openai/chat -run "ChatHistory" -count=1
```

Expected: PASS.

**Step 5: Commit**

```powershell
git add internal/chathistory/store.go internal/chathistory/store_test.go internal/httpapi/openai/chat/chat_history_test.go
git commit -m "feat: align chat history retention with concurrency"
```

---

### Task 4: Add remote session registry tests

**Files:**
- Create or modify: `internal/httpapi/openai/chat/remote_session_retention_test.go`
- Modify later: `internal/httpapi/openai/chat/handler_chat.go`

**Step 1: Test that active session is never deleted**

Scenario:

```text
session A = active
session B = success
limit = 1
```

Expected:
- pruning never calls `DeleteSessionForToken(A)`
- if B is within limit, B also remains

**Step 2: Test that only old success sessions are deleted**

Scenario:

```text
success sessions A, B, C
limit = 2
```

Expected:
- delete oldest success session only
- keep newest two

**Step 3: Test unknown remote sessions are protected**

Scenario:

```text
remote session X exists upstream but registry has no X
```

Expected:
- X is not a candidate because registry-only prune cannot see or prove it is successful
- no `FetchSessionPage` dependency is added to `shared.DeepSeekCaller`

**Step 4: Test failed session is protected**

Scenario:

```text
session A marked failed/error
limit = 0 or 1
```

Expected:
- A is not automatically deleted by success-only retention

**Step 5: Test TTL deletes only successful records**

Scenario:

```text
session A = success, UpdatedAt older than remoteSessionRetentionDelay
session B = active, UpdatedAt older than remoteSessionRetentionDelay
```

Expected:
- A is deleted
- B is preserved

**Step 6: Test terminal-state mapping for pre-retry early returns**

After `CreateSession` succeeds, cover early returns before retry handlers run:
- `GetPow` failure → registry state `failed`, no delete
- initial `CallCompletion` failure → registry state `failed`, no delete
- request context cancellation/client disconnect before completion → registry state `stopped`, no delete

Expected:
- none of these paths are marked `success`
- none of these paths call `DeleteSessionForToken`

**Step 7: Test retry terminal-state mapping**

Cover direct retry behavior:
- non-stream empty/malformed first attempt then synthetic retry success → `success`
- non-stream retry ultimately fails → `failed`
- stream empty/malformed first attempt then retry success → `success`
- stream retry response non-200 or retry `CallCompletion` error → `failed`
- stream context canceled → `stopped`

Expected:
- only final successful outputs become `success`
- failed/stopped retry paths are not deleted by retention

**Step 8: Run targeted tests and verify failure**

Run:

```powershell
go test ./internal/httpapi/openai/chat -run "RemoteSessionRetention|AutoDelete" -count=1
```

Expected: FAIL until registry/prune code exists.

**Step 9: Update existing retention tests**

Update `internal/httpapi/openai/chat/handler_chat_auto_delete_test.go` expectations:
- default retention must not call `DeleteAllSessionsForToken`
- default retention must not delete an active session immediately
- delayed deletion should happen only after the session is marked `success`

---

### Task 5: Implement remote success-only registry and prune

**Files:**
- Modify: `internal/httpapi/openai/chat/handler_chat.go`
- Modify: `internal/httpapi/openai/chat/empty_retry_runtime.go`
- Modify: `internal/httpapi/openai/chat/handler.go`
- Possibly create: `internal/httpapi/openai/chat/remote_session_retention.go`
- Test: `internal/httpapi/openai/chat/remote_session_retention_test.go`

**Step 1: Add minimal in-process registry**

Prefer a small private type in the `chat` package:

```go
type remoteSessionState string

const (
	remoteSessionActive    remoteSessionState = "active"
	remoteSessionSuccess   remoteSessionState = "success"
	remoteSessionFailed    remoteSessionState = "failed"
	remoteSessionStopped   remoteSessionState = "stopped"
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
```

Do not store raw tokens as map keys if avoidable; use a stable hash.

Add the registry to `Handler` in `internal/httpapi/openai/chat/handler.go`:

```go
remoteSessionsOnce sync.Once
remoteSessions     *remoteSessionRegistry
```

Use a helper such as `h.remoteSessionRegistry()` to lazily initialize it so existing `Handler{...}` test literals do not need to be updated:

```go
func (h *Handler) remoteSessionRegistry() *remoteSessionRegistry {
	if h == nil {
		return nil
	}
	h.remoteSessionsOnce.Do(func() {
		h.remoteSessions = newRemoteSessionRegistry()
	})
	return h.remoteSessions
}
```

Do not use unguarded lazy initialization; `Handler` is shared by concurrent requests and a plain nil check/write would race.

**Step 2: Register after successful create**

Immediately after:

```go
sessionID, err = h.DS.CreateSession(...)
```

record:

```text
state = active
```

**Step 3: Add explicit terminal-state reporting**

Do not mark remote sessions from a defer in `ChatCompletions`; that would misclassify error responses as successful.

Return `remoteSessionTerminalState` from:

```go
handleNonStreamWithRetry(...)
handleStreamWithRetry(...)
```

The terminal values should be:

```go
type remoteSessionTerminalState string

const (
	remoteTerminalSuccess remoteSessionTerminalState = "success"
	remoteTerminalFailed  remoteSessionTerminalState = "failed"
	remoteTerminalStopped remoteSessionTerminalState = "stopped"
)
```

Map outcomes explicitly:
- non-stream writes HTTP 200 body → `success`
- stream finalizes and sends `[DONE]` without `finalErrorMessage` → `success`
- upstream non-200, empty-output terminal error, retry failure, PoW/session/completion errors → `failed`
- request context cancellation / client disconnect path → `stopped`

Only `success` becomes a deletion candidate.

Update every direct test call site for these functions after changing signatures, especially:
- `internal/httpapi/openai/chat/handler_toolcall_test.go`
- any other tests matching `handleNonStreamWithRetry(` or `handleStreamWithRetry(`

Tests that do not care about retention may ignore the returned value with `_ = ...`, but success/failure retention tests should assert it.

**Step 3a: Mark pre-retry early returns in `ChatCompletions`**

For paths after `CreateSession` succeeds but before entering retry handlers, mark state explicitly:
- `GetPow` failure → `remoteSessionFailed`
- initial `CallCompletion` failure → `remoteSessionFailed`
- request context cancellation/client disconnect → `remoteSessionStopped`

These paths must not remain `active` forever and must not be marked `success`.

**Step 4: Replace pre-create delete-all**

Delete or disable this behavior in retention mode:

```go
h.deletePreviousRemoteSessionsForRetention(...)
```

It must no longer call `DeleteAllSessionsForToken` for retention mode.

**Step 5: Add registry-only success prune function**

Implement:

```go
func (h *Handler) pruneSuccessfulRemoteSessions(ctx context.Context, a *auth.RequestAuth)
```

Behavior:
1. get retention limit from a chat-local narrow interface helper, not from `shared.ConfigReader`
2. read candidates from the in-process registry only
3. keep only records with `State == remoteSessionSuccess`
4. sort successful candidates newest-first
5. delete candidates that are outside the newest `limit`
6. also delete successful candidates whose `UpdatedAt` is older than `remoteSessionRetentionDelay`
7. call `DeleteSessionForToken` only for those candidates
8. remove successfully deleted records from the registry

Do not add `FetchSessionPage` to `shared.DeepSeekCaller`; unknown upstream sessions are protected by being invisible to registry-only prune.

**Step 6: Call prune after terminal state is known**

Call after marking a session `success`. Do not prune before creating a new session.

**Step 7: Preserve TTL deletion only for success sessions**

If keeping the existing 10-minute delayed deletion behavior, schedule it only when the session is marked `success`. The delayed job must re-check registry state before deleting:

```text
delete if registry still says state == success and age >= remoteSessionRetentionDelay
```

This means:
- active sessions are never deleted by TTL
- failed/stopped sessions are never deleted by TTL
- unknown/not-in-registry sessions are never deleted by TTL

**Step 8: Run tests**

Run:

```powershell
go test ./internal/httpapi/openai/chat -run "RemoteSessionRetention|AutoDelete" -count=1
```

Expected: PASS.

**Step 8a: Run compile-sensitive direct-call tests**

Run:

```powershell
go test ./internal/httpapi/openai/chat -run "Tool|Retry|RemoteSessionRetention|AutoDelete" -count=1
```

Expected: PASS. This catches stale direct calls to `handleNonStreamWithRetry` / `handleStreamWithRetry`.

**Step 9: Commit**

```powershell
git add internal/httpapi/openai/chat/handler.go internal/httpapi/openai/chat/handler_chat.go internal/httpapi/openai/chat/empty_retry_runtime.go internal/httpapi/openai/chat/remote_session_retention*.go internal/httpapi/openai/chat/*retention*_test.go
git commit -m "fix: retain only successful remote sessions"
```

---

### Task 6: Update admin/UI retention copy

**Files:**
- Modify: `webui/src/locales/zh.json`
- Modify: `webui/src/locales/en.json`
- Test: none required unless locale tests exist

**Step 1: Replace inaccurate copy**

Current copy says retention is locked to one session. Update it to describe:

Chinese:

```text
调试会话保留会跟随每账号并发上限；自动清理只会删除旧的成功会话，运行中、失败、中止或状态未知的会话不会被自动删除。
```

English:

```text
Debug session retention follows the per-account concurrency limit. Automatic cleanup deletes only old successful sessions; running, failed, stopped, or unknown sessions are preserved.
```

**Step 2: Commit**

```powershell
git add webui/src/locales/zh.json webui/src/locales/en.json
git commit -m "docs: clarify success-only session retention"
```

---

### Task 7: Full verification

**Files:**
- No code changes expected.

**Step 1: Run targeted tests**

```powershell
go test ./internal/chathistory ./internal/httpapi/openai/chat -count=1
```

Expected: PASS.

**Step 2: Run broader backend tests if feasible**

```powershell
go test ./internal/... -count=1
```

Expected: PASS.

**Step 3: Inspect diff**

```powershell
git status
git diff --stat
git diff
```

Expected:
- no accidental unrelated changes
- no `DeleteAllSessionsForToken` call remains in retention mode
- success-only deletion is explicit and tested

**Step 4: Final commit if needed**

If earlier task commits were not made, commit all changes:

```powershell
git add internal webui docs
git commit -m "fix: make session retention success-only"
```

---

## Acceptance Criteria

- Same-account concurrent requests cannot delete each other’s active remote sessions.
- Local history retention never deletes `streaming`, `running`, `error`, `failed`, `stopped`, `cancelled`, or unknown-status entries, even when they are older than the TTL.
- Remote retention never deletes unknown remote sessions after process restart.
- Retention limit defaults to per-account max inflight.
- Tests prove success-only pruning for local and remote paths.
- UI copy no longer claims retention is hardcoded to one session.

## Review-Driven Implementation Notes

- Do not expand `shared.ConfigReader` for this feature; use a chat-local optional interface to read `RuntimeAccountMaxInflight()`.
- Do not use `FetchSessionPage`; registry-only retention intentionally protects unknown remote sessions.
- Do not infer success from `CompletedAt`; success means `status == "success"` locally and `state == remoteSessionSuccess` remotely.
- Do not mark remote success from `defer`; retry handlers must return explicit terminal state.
- Do not leave sessions active on post-`CreateSession` early returns; mark them `failed` or `stopped`.
- Do not lazily initialize `Handler.remoteSessions` without `sync.Once` or equivalent locking.
- Update direct-call tests for `handleNonStreamWithRetry` and `handleStreamWithRetry` after adding return values.

## NOT in Scope

- Distributed/multi-instance shared session registry.
- UI controls for custom retention limit.
- Automatic cleanup of failed/error/cancelled sessions.
- Changing DeepSeek upstream session creation semantics.
