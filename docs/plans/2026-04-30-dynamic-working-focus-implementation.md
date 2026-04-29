# Dynamic Working Focus Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Prevent repeated answers and stale assistant continuation by making request-local `WORKING STATE` explicitly choose latest-user focus or active assistant-tail focus.

**Architecture:** Keep the existing request-local `HISTORY.txt` upload flow, but harden `internal/promptcompat/history_transcript.go` so the generated working state has explicit modes, statuses, and non-revival rules. Add regression tests in the existing OpenAI history split suite before changing implementation.

**Tech Stack:** Go, `go test`, existing `promptcompat` transcript builder, existing OpenAI current-input-file tests.

---

## Context

Read first:

- Design doc: `docs/plans/2026-04-30-dynamic-working-focus-design.md`
- Transcript implementation: `internal/promptcompat/history_transcript.go`
- Current-input upload flow: `internal/httpapi/openai/history/current_input_file.go`
- Existing tests: `internal/httpapi/openai/history_split_test.go`

Existing important behavior:

- `BuildOpenAICurrentInputContextTranscript` builds the `HISTORY.txt` body.
- `buildAgentWorkingState` already chooses `answer_latest_user` when the last non-empty message is user, otherwise `continue_agent_tail`.
- `workingStateMessages` already avoids including old assistant turns in latest-user mode.
- The bug risk is that `continue_agent_tail` is too broad: a final assistant answer after the last user can still be treated as active work.

Target behavior:

```text
last user message exists and is latest non-empty message
  -> Mode: answer_latest_user
  -> working includes only latest user message

latest non-empty message is tool or assistant tool-call / progress
  -> Mode: continue_agent_tail
  -> working includes assistant/tool tail after latest user

latest non-empty message is plain final assistant answer
  -> Mode: no_active_working
  -> working contains none
  -> instruction says do not repeat completed answer
```

---

### Task 1: Add failing tests for final assistant answer non-revival

**Files:**

- Modify: `internal/httpapi/openai/history_split_test.go`

**Step 1: Add the failing test**

Append near the existing `TestBuildOpenAICurrentInputContextTranscriptUsesAssistantTailWhenLatestIsNotUser` tests:

```go
func TestBuildOpenAICurrentInputContextTranscriptDoesNotReviveFinalAssistantAnswer(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "问题 A"},
		map[string]any{"role": "assistant", "content": "这是 A 的最终回答。"},
	}

	transcript := buildOpenAICurrentInputContextTranscript(messages)

	for _, want := range []string{
		"Mode:\n- no_active_working",
		"Latest assistant/tool tail:\n- none",
		"No active assistant task is pending. Do not repeat a completed answer.",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected completed assistant answer not to be active working %q, got %q", want, transcript)
		}
	}
	workingStart := strings.Index(transcript, "=== WORKING STATE, READ FIRST ===")
	fullStart := strings.Index(transcript, "=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===")
	if workingStart < 0 || fullStart < 0 || fullStart <= workingStart {
		t.Fatalf("expected working and full context sections, got %q", transcript)
	}
	working := transcript[workingStart:fullStart]
	if strings.Contains(working, "这是 A 的最终回答。") {
		t.Fatalf("completed assistant answer must not be injected as working state, got %q", working)
	}
	if !strings.Contains(transcript[fullStart:], "这是 A 的最终回答。") {
		t.Fatalf("completed answer may remain in reference-only chronological context, got %q", transcript)
	}
}
```

**Step 2: Run the test to verify it fails**

Run:

```bash
go test ./internal/httpapi/openai -run TestBuildOpenAICurrentInputContextTranscriptDoesNotReviveFinalAssistantAnswer -count=1
```

Expected: FAIL because current code reports `Mode: continue_agent_tail` and includes the final assistant answer as active working.

**Step 3: Commit the failing test**

```bash
git add internal/httpapi/openai/history_split_test.go
git commit -m "test: cover completed assistant working revival"
```

---

### Task 2: Add explicit working focus classification

**Files:**

- Modify: `internal/promptcompat/history_transcript.go`

**Step 1: Update mode documentation in the read policy**

In `buildActiveAgentResumeTranscript`, add a third mode line after the existing mode bullets:

```go
b.WriteString("- If Mode is no_active_working, do not continue or repeat completed assistant output.\n")
```

**Step 2: Update `workingStateNextAction`**

Replace the function with:

```go
func workingStateNextAction(mode string) string {
	switch mode {
	case "answer_latest_user":
		return "Answer the latest user message. Do not continue stale assistant/tool working state."
	case "no_active_working":
		return "No active assistant task is pending. Do not repeat a completed answer."
	default:
		return "Continue from the latest assistant/tool tail. Do not restart from the original user request or repeat earlier answers."
	}
}
```

**Step 3: Add final-answer classifier helpers**

Add near `buildAgentWorkingState`:

```go
func determineWorkingMode(messages []map[string]any, lastIndex int) string {
	if lastIndex < 0 {
		return "no_active_working"
	}
	last := messages[lastIndex]
	role := strings.ToLower(strings.TrimSpace(asString(last["role"])))
	switch role {
	case "user":
		return "answer_latest_user"
	case "tool", "function":
		return "continue_agent_tail"
	case "assistant":
		if assistantMessageHasPendingWork(last) {
			return "continue_agent_tail"
		}
		return "no_active_working"
	default:
		return "no_active_working"
	}
}

func assistantMessageHasPendingWork(msg map[string]any) bool {
	if hasNonEmptyToolCalls(msg["tool_calls"]) {
		return true
	}
	content := strings.ToLower(transcriptMessageContent(msg))
	return strings.Contains(content, "<|dsml|tool_calls>")
}

func hasNonEmptyToolCalls(raw any) bool {
	switch v := raw.(type) {
	case nil:
		return false
	case []any:
		return len(v) > 0
	case []map[string]any:
		return len(v) > 0
	default:
		return strings.TrimSpace(fmt.Sprint(v)) != ""
	}
}
```

**Step 4: Use the classifier in `buildAgentWorkingState`**

Replace:

```go
mode := "continue_agent_tail"
if lastIndex >= 0 && strings.EqualFold(strings.TrimSpace(asString(messages[lastIndex]["role"])), "user") {
	mode = "answer_latest_user"
}
```

with:

```go
mode := determineWorkingMode(messages, lastIndex)
```

**Step 5: Make `workingStateMessages` return no active working for completed answers**

At the top of `workingStateMessages`, after the empty/lastIndex guard:

```go
if mode == "no_active_working" {
	return nil
}
```

**Step 6: Run the focused test**

Run:

```bash
go test ./internal/httpapi/openai -run TestBuildOpenAICurrentInputContextTranscriptDoesNotReviveFinalAssistantAnswer -count=1
```

Expected: PASS.

**Step 7: Run nearby transcript tests**

Run:

```bash
go test ./internal/httpapi/openai -run 'TestBuildOpenAICurrentInputContextTranscript|TestSplitOpenAIHistoryMessagesUsesLatestUserTurn|TestApplyCurrentInputFileUploadsFirstTurnWithInjectedWrapper' -count=1
```

Expected: PASS.

**Step 8: Commit**

```bash
git add internal/promptcompat/history_transcript.go
git commit -m "fix: avoid reviving completed assistant working state"
```

---

### Task 3: Add regression tests for active assistant/tool continuation

**Files:**

- Modify: `internal/httpapi/openai/history_split_test.go`

**Step 1: Add assistant tool-call continuation test**

Append:

```go
func TestBuildOpenAICurrentInputContextTranscriptContinuesAssistantToolCall(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "查一下资料"},
		map[string]any{
			"role": "assistant",
			"content": "",
			"tool_calls": []any{
				map[string]any{"name": "search", "arguments": map[string]any{"query": "docs"}},
			},
		},
	}

	transcript := buildOpenAICurrentInputContextTranscript(messages)

	for _, want := range []string{
		"Mode:\n- continue_agent_tail",
		"Latest assistant/tool tail:",
		"<|DSML|tool_calls>",
		"Waiting for tool result",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected assistant tool call to remain active %q, got %q", want, transcript)
		}
	}
}
```

**Step 2: Add tool result continuation test**

Append:

```go
func TestBuildOpenAICurrentInputContextTranscriptContinuesToolResult(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "查一下资料"},
		map[string]any{
			"role": "assistant",
			"content": "<|DSML|tool_calls>\n<invoke name=\"search\"></invoke>",
		},
		map[string]any{"role": "tool", "content": "搜索结果"},
	}

	transcript := buildOpenAICurrentInputContextTranscript(messages)

	for _, want := range []string{
		"Mode:\n- continue_agent_tail",
		"Latest assistant/tool tail:",
		"[Tool]\n搜索结果",
		"Reviewing latest tool result",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("expected tool result to remain active %q, got %q", want, transcript)
		}
	}
}
```

**Step 3: Run the new tests**

Run:

```bash
go test ./internal/httpapi/openai -run 'TestBuildOpenAICurrentInputContextTranscriptContinues(AssistantToolCall|ToolResult)' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/httpapi/openai/history_split_test.go
git commit -m "test: cover active assistant working continuation"
```

---

### Task 4: Harden latest-user interruption behavior

**Files:**

- Modify: `internal/httpapi/openai/history_split_test.go`
- Modify if needed: `internal/promptcompat/history_transcript.go`

**Step 1: Add latest-user interruption regression test**

Append:

```go
func TestBuildOpenAICurrentInputContextTranscriptLatestUserInterruptsAssistantTail(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "先做 A"},
		map[string]any{"role": "assistant", "content": "我准备继续 A"},
		map[string]any{"role": "user", "content": "别做 A 了，改做 B"},
	}

	transcript := buildOpenAICurrentInputContextTranscript(messages)

	workingStart := strings.Index(transcript, "=== WORKING STATE, READ FIRST ===")
	fullStart := strings.Index(transcript, "=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===")
	if workingStart < 0 || fullStart < 0 || fullStart <= workingStart {
		t.Fatalf("expected working and full context sections, got %q", transcript)
	}
	working := transcript[workingStart:fullStart]
	for _, want := range []string{
		"Mode:\n- answer_latest_user",
		"Latest user message:\n[User]\n别做 A 了，改做 B",
		"Answer the latest user message. Do not continue stale assistant/tool working state.",
	} {
		if !strings.Contains(working, want) {
			t.Fatalf("expected latest user to interrupt assistant tail %q, got %q", want, working)
		}
	}
	if strings.Contains(working, "我准备继续 A") {
		t.Fatalf("stale assistant tail must not drive latest-user request, got %q", working)
	}
}
```

**Step 2: Run the test**

Run:

```bash
go test ./internal/httpapi/openai -run TestBuildOpenAICurrentInputContextTranscriptLatestUserInterruptsAssistantTail -count=1
```

Expected: PASS if current latest-user behavior is already correct. If it fails, fix `workingStateMessages` so `answer_latest_user` returns only `messages[lastIndex]`.

**Step 3: Commit**

```bash
git add internal/httpapi/openai/history_split_test.go internal/promptcompat/history_transcript.go
git commit -m "test: cover latest user interruption focus"
```

---

### Task 5: Harden current input file prompt wording

**Files:**

- Modify: `internal/httpapi/openai/history/current_input_file.go`
- Modify: `internal/httpapi/openai/history_split_test.go`

**Step 1: Update neutral prompt text**

In `currentInputFilePrompt`, include explicit completed-working language:

```go
return "Attached context belongs only to the current API request. Use only the attached request-local context and the latest user message. Read HISTORY.txt first and follow its WORKING STATE section. If WORKING STATE says no_active_working, do not repeat completed assistant answers. Do not use account-level memories, recent chats, previous sessions, or files not listed in ref_file_ids. If the latest user message explicitly asks to continue prior work, use the attached request-local context to continue. Otherwise, answer the latest user message directly."
```

In `currentInputFilePromptWithTools`, preserve existing tool syntax instructions and add the same `HISTORY.txt` / `no_active_working` sentence before the account-memory warning.

**Step 2: Add/adjust assertions**

In `TestApplyCurrentInputFileUploadsFirstTurnWithInjectedWrapper` or a nearby current-input-file test, assert:

```go
if !strings.Contains(out.FinalPrompt, "follow its WORKING STATE section") {
	t.Fatalf("expected live prompt to instruct model to follow HISTORY working state, got %s", out.FinalPrompt)
}
if !strings.Contains(out.FinalPrompt, "no_active_working") {
	t.Fatalf("expected live prompt to mention no_active_working, got %s", out.FinalPrompt)
}
```

**Step 3: Run current-input-file tests**

Run:

```bash
go test ./internal/httpapi/openai -run 'TestApplyCurrentInputFile|TestBuildOpenAIToolPromptFileTranscript' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/httpapi/openai/history/current_input_file.go internal/httpapi/openai/history_split_test.go
git commit -m "fix: clarify current input working-state prompt"
```

---

### Task 6: Run full verification

**Files:**

- No edits unless failures reveal required fixes.

**Step 1: Run package tests**

Run:

```bash
go test ./internal/promptcompat ./internal/httpapi/openai ./internal/prompt ./internal/util
```

Expected: PASS.

**Step 2: Run all tests**

Run:

```bash
go test ./...
```

Expected: PASS.

**Step 3: Inspect diff**

Run:

```bash
git diff --stat HEAD~4..HEAD
git diff HEAD~4..HEAD -- internal/promptcompat/history_transcript.go internal/httpapi/openai/history/current_input_file.go internal/httpapi/openai/history_split_test.go
```

Expected: Only focused transcript, prompt wording, and regression test changes.

**Step 4: Final commit if any verification-only fixes were needed**

If verification required extra fixes:

```bash
git add <changed-files>
git commit -m "fix: stabilize dynamic working focus tests"
```

Otherwise do not create an empty commit.

---

## Implementation Notes

- Keep the diff small. Do not introduce persistence for `working.json` in this pass unless tests prove it is necessary.
- Do not change file upload names; `HISTORY.txt` and `TOOL_PROMPT.txt` should remain stable.
- Do not remove full chronological context. It is still useful as reference, but the read policy must say not to treat it as active driver.
- If a test fixture currently expects `continue_agent_tail` for plain final assistant answers, update that expectation to `no_active_working`; that old expectation is the bug.

## Completion Checklist

- [ ] Final assistant answer is reference-only, not active working.
- [ ] Latest user message interrupts stale assistant tail.
- [ ] Assistant tool calls and tool results still continue correctly.
- [ ] Current input prompt tells the model to follow `WORKING STATE`.
- [ ] Focused tests pass.
- [ ] `go test ./...` passes.
