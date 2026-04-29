# Empty Read Parameters Fix Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Prevent resumed agents from emitting empty `Read.file_path` calls by preserving useful read paths and strengthening resume guidance.

**Architecture:** Keep the existing transcript scrubber, but extract file paths from scrubbed `Read/Reading` UI result lines before removing noisy code/tool output. Add targeted resume guidance so models search/locate files instead of issuing empty reads when context lacks a path.

**Tech Stack:** Go transcript normalization and OpenAI current-input history tests.

---

### Task 1: Preserve read-result paths during transcript normalization

**Files:**
- Modify: `internal/promptcompat/history_transcript.go`
- Modify: `internal/promptcompat/internal_tool_event_filter.go`
- Test: `internal/httpapi/openai/history_split_test.go`

**Step 1: Write the failing test**

Add a test that builds current-input transcript from an assistant message containing a scrubbed Read UI block:

```go
func TestBuildOpenAICurrentInputContextTranscriptPreservesReadUIPaths(t *testing.T) {
    messages := []any{
        map[string]any{"role": "user", "content": "继续修复"},
        map[string]any{"role": "assistant", "content": "先读目标文件。\n\n● Read 1 file (ctrl+o to expand)\n  ⎿  internal\\provider\\stream_translator.go"},
    }
    transcript := buildOpenAICurrentInputContextTranscript(messages)

    if !strings.Contains(transcript, "Already read files:\n- internal\\provider\\stream_translator.go") {
        t.Fatalf("expected read UI path to be preserved in working state, got %q", transcript)
    }
    if strings.Contains(transcript, "Read 1 file") {
        t.Fatalf("expected Read UI noise to stay scrubbed, got %q", transcript)
    }
}
```

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/httpapi/openai -run TestBuildOpenAICurrentInputContextTranscriptPreservesReadUIPaths -count=1
```

Expected: FAIL because the path is scrubbed with the UI block.

**Step 3: Implement minimal extraction**

Add a helper that scans raw message content for `⎿ path` lines around Read UI output and merges those paths into `ReadFiles`, while keeping scrubbed transcript output unchanged.

**Step 4: Run target test**

Run the same `go test` command. Expected: PASS.

### Task 2: Strengthen resume guidance when paths are missing

**Files:**
- Modify: `internal/promptcompat/history_transcript.go`
- Test: `internal/httpapi/openai/history_split_test.go`

**Step 1: Write the failing test**

Add an assertion that the working-state transcript tells the model not to call `Read` with an empty path and to locate/search first.

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/httpapi/openai -run TestBuildOpenAICurrentInputContextTranscriptBuildsActiveAgentResumePackage -count=1
```

Expected: FAIL until guidance text is added.

**Step 3: Implement prompt guidance**

Add one read-policy bullet:

```text
- If you need to read a file but no concrete path is available, locate it first with search/glob; never call Read with an empty file_path.
```

**Step 4: Run target tests**

Run:

```bash
go test ./internal/httpapi/openai ./internal/promptcompat -count=1
```

Expected: PASS.
