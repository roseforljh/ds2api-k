# Remote Session Retention Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Keep only the newest DeepSeek official Web session created by this project, then delete it after 10 minutes.

**Architecture:** Use the existing DeepSeek delete APIs. Before creating a new remote session, delete all existing remote sessions for the token so the next session becomes the only visible official Web session. After creating it, schedule delayed deletion of that session instead of deleting it at request completion.

**Tech Stack:** Go HTTP handlers, existing `deepseek/client` session delete methods, table-driven Go tests.

---

### Task 1: Add scheduled remote retention behavior

**Files:**
- Modify: `internal/httpapi/openai/chat/handler_chat.go`
- Test: `internal/httpapi/openai/chat/handler_chat_auto_delete_test.go`

**Steps:**
1. Write a failing test proving default/empty config deletes old sessions before create and does not delete current session immediately.
2. Write a failing test proving a scheduled delete uses `DeleteSessionForToken` for the created session.
3. Implement pre-create `DeleteAllSessionsForToken` when retention mode is active.
4. Implement delayed single-session deletion helper with a testable delay constant.
5. Run targeted chat auto-delete tests.

### Task 2: Lock config/admin semantics to retention mode

**Files:**
- Modify: `internal/config/store_accessors.go`
- Modify: `internal/httpapi/admin/settings/handler_settings_parse.go`
- Test: `internal/config/config_edge_test.go`
- Test: `internal/httpapi/admin/handler_settings_test.go`

**Steps:**
1. Update tests so default `auto_delete` resolves to the retention behavior, not `none`.
2. Ensure admin setting updates no longer silently force immediate all-delete-at-end behavior.
3. Run targeted config/admin tests.

### Task 3: Verify local debug history remains independent

**Files:**
- Test: `internal/chathistory/store_test.go`
- Test: `internal/httpapi/openai/chat/chat_history_test.go`

**Steps:**
1. Run existing targeted local chat-history tests.
2. Run combined targeted test suite for remote retention and local history capture.
