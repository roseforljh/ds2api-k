# Tool Prompt File Split Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Move the large tool/agent prompt generated from `ToolsRaw` out of the visible DeepSeek `payload.prompt` and into a separate uploaded text attachment when `current_input_file` is active.

**Architecture:** Keep `HISTORY.txt` as the dynamic conversation-context file. Add an optional second `TOOL_PROMPT.txt` upload containing the exact tool instructions currently injected by `promptcompat.injectToolPrompt`, as normal downloadable file content. The live `payload.prompt` becomes a short activation prompt that does not reference concrete file names. Tool names must still be returned for stream/tool parsing.

**Tech Stack:** Go, existing `promptcompat` prompt builder, existing DeepSeek file upload client, existing config/admin settings plumbing, existing OpenAI/Claude compatibility tests.

---

## Current Root Cause

The visible prompt in `bug.txt` is not from `stdReq.Messages` directly. It is generated after `HISTORY` upload because `ApplyCurrentInputFile` rebuilds `FinalPrompt` with `stdReq.ToolsRaw`:

```go
stdReq.FinalPrompt, stdReq.ToolNames =
    promptcompat.BuildOpenAIPrompt(messages, stdReq.ToolsRaw, "", stdReq.ToolChoice, stdReq.Thinking)
```

That calls `promptcompat.injectToolPrompt`, which prepends a system message:

```text
You have access to these tools:
Tool: Agent
Description: ...
```

Therefore the current upstream shape is:

```text
ref_file_ids = [HISTORY, user files...]
payload.prompt = giant tool system prompt + neutral user prompt
```

The target shape is:

```text
ref_file_ids = [tool-instruction TOOL_PROMPT.txt, context HISTORY.txt, user files...]
payload.prompt = short activation user prompt
```

---

## Non-Goals

- Do not split arbitrary user messages.
- Do not try to semantically detect “Claude Code prompt”.
- Do not remove tool support.
- Do not change behavior when the new option is disabled.
- Do not change DeepSeek upload API semantics.

---

## Configuration Design

Extend `current_input_file`:

```json
{
  "current_input_file": {
    "enabled": true,
    "min_chars": 0,
    "tool_prompt_file": true
  }
}
```

Default should be `false` for safe rollout. When false, current behavior remains byte-for-byte as close as possible.

Accessor:

```go
CurrentInputToolPromptFileEnabled() bool
```

Interface additions:

- `internal/httpapi/openai/shared/deps.go`
- `internal/config/store_accessors.go`
- admin settings read/write/parse
- Web UI settings only after backend tests pass

---

## Task 1: Extract Tool Prompt Builder Without Changing Behavior

**Files:**
- Modify: `internal/promptcompat/tool_prompt.go`
- Add/Modify tests: `internal/promptcompat/prompt_build_test.go` or new `internal/promptcompat/tool_prompt_test.go`

**Step 1: Write failing tests**

Add tests for a new exported function:

```go
func BuildOpenAIToolPrompt(toolsRaw any, policy ToolChoicePolicy) (string, []string)
```

Test cases:

1. No tools returns `("", nil)`.
2. One function tool returns text containing:
   - `You have access to these tools:`
   - `Tool: <name>`
   - `Description: <description>`
   - tool-call instructions from `toolcall.BuildToolCallInstructions`
3. `ToolChoiceNone` returns empty prompt and nil names.
4. Required tool choice appends the “MUST call at least one tool” line.
5. Forced tool choice appends the “MUST call exactly this tool name” line.

Run:

```powershell
go test ./internal/promptcompat
```

Expected: FAIL because function does not exist.

**Step 2: Implement minimal extraction**

Refactor `injectToolPrompt` so it uses the new function:

```go
func BuildOpenAIToolPrompt(toolsRaw any, policy ToolChoicePolicy) (string, []string) {
    tools, ok := toolsRaw.([]any)
    if !ok || len(tools) == 0 || policy.IsNone() {
        return "", nil
    }
    // Move existing schema/name collection logic here.
    // Return toolPrompt, names.
}
```

Then:

```go
func injectToolPrompt(messages []map[string]any, tools []any, policy ToolChoicePolicy) ([]map[string]any, []string) {
    toolPrompt, names := BuildOpenAIToolPrompt(tools, policy)
    if strings.TrimSpace(toolPrompt) == "" {
        return messages, names
    }
    ...
}
```

**Step 3: Verify no behavior changed**

Run:

```powershell
go test ./internal/promptcompat ./internal/httpapi/openai
```

Expected: PASS.

---

## Task 2: Add Backend Configuration Plumbing

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/store_accessors.go`
- Modify: `internal/config/validation.go`
- Modify: `internal/httpapi/openai/shared/deps.go`
- Modify: `internal/httpapi/admin/settings/handler_settings_read.go`
- Modify: `internal/httpapi/admin/settings/handler_settings_parse.go`
- Modify: `internal/httpapi/admin/settings/handler_settings_write.go`
- Tests: `internal/config/store_accessors_test.go`, `internal/httpapi/admin/handler_settings_test.go`

**Step 1: Add field**

```go
type CurrentInputFileConfig struct {
    Enabled        *bool `json:"enabled,omitempty"`
    MinChars       int   `json:"min_chars,omitempty"`
    ToolPromptFile *bool `json:"tool_prompt_file,omitempty"`
}
```

Use pointer bool so omitted means default false.

**Step 2: Add accessor**

```go
func (s *Store) CurrentInputToolPromptFileEnabled() bool {
    s.mu.RLock()
    defer s.mu.RUnlock()
    if s.cfg.CurrentInputFile.ToolPromptFile == nil {
        return false
    }
    return *s.cfg.CurrentInputFile.ToolPromptFile
}
```

Add this to `shared.ConfigReader`.

**Step 3: Admin read/parse/write**

Settings read should include:

```go
"tool_prompt_file": h.Store.CurrentInputToolPromptFileEnabled(),
```

Settings parse should accept:

```go
if v, exists := raw["tool_prompt_file"]; exists {
    b := boolFrom(v)
    cfg.ToolPromptFile = &b
}
```

Settings write should preserve partial updates like existing `enabled` / `min_chars`.

**Step 4: Tests**

Add:

- default false
- explicit true
- partial update preserves existing value
- read endpoint returns field

Run:

```powershell
go test ./internal/config ./internal/httpapi/admin ./internal/httpapi/openai
```

Expected: PASS.

---

## Task 3: Upload Tool Prompt File in Current Input Mode

**Files:**
- Modify: `internal/httpapi/openai/history/current_input_file.go`
- Modify: `internal/httpapi/openai/history/history_split.go`
- Tests: `internal/httpapi/openai/history_split_test.go`

**Step 1: Add constants**

```go
const (
    currentInputToolFilename = "TOOL_PROMPT.txt"
)
```

Use same content type and purpose as `HISTORY`:

```go
text/plain; charset=utf-8
assistants
```

**Step 2: Add helper to prepend multiple refs in stable order**

Current helper prepends one file. Need stable final order:

```text
tool file first
history file second
existing user files after
```

Add:

```go
func prependUniqueRefFileIDs(existing []string, fileIDs ...string) []string
```

Implementation should preserve order of `fileIDs`, trim blanks, dedupe case-insensitively, then append existing non-duplicates.

**Step 3: Add tool file branch**

Inside `ApplyCurrentInputFile`, after uploading `HISTORY` and before rebuilding `FinalPrompt`:

```go
toolNames := stdReq.ToolNames
toolsRawForPrompt := stdReq.ToolsRaw
toolFileID := ""

if s.Store.CurrentInputToolPromptFileEnabled() {
    toolText, names := promptcompat.BuildOpenAIToolPrompt(stdReq.ToolsRaw, stdReq.ToolChoice)
    if strings.TrimSpace(toolText) != "" {
        result, err := s.DS.UploadFile(ctx, a, dsclient.UploadFileRequest{
            Filename:    currentInputToolFilename,
            ContentType: currentInputContentType,
            Purpose:     currentInputPurpose,
            Data:        []byte(promptcompat.BuildOpenAIToolPromptFileTranscript(toolText)),
        }, 3)
        if err != nil {
            return stdReq, fmt.Errorf("upload current input tool prompt file: %w", err)
        }
        toolFileID = strings.TrimSpace(result.ID)
        if toolFileID == "" {
            return stdReq, errors.New("upload current input tool prompt file returned empty file id")
        }
        toolNames = names
        toolsRawForPrompt = nil
    }
}
```

Then set refs:

```go
if toolFileID != "" {
    stdReq.RefFileIDs = prependUniqueRefFileIDs(stdReq.RefFileIDs, toolFileID, fileID)
} else {
    stdReq.RefFileIDs = prependUniqueRefFileID(stdReq.RefFileIDs, fileID)
}
```

Then build final prompt:

```go
stdReq.FinalPrompt, builtToolNames =
    promptcompat.BuildOpenAIPrompt(messages, toolsRawForPrompt, "", stdReq.ToolChoice, stdReq.Thinking)
if toolFileID != "" {
    stdReq.ToolNames = toolNames
} else {
    stdReq.ToolNames = builtToolNames
}
```

**Important:** Do not lose `stdReq.ToolNames`; streaming/tool parsing depends on it.

**Step 4: Activation prompt**

When tool file is enabled and uploaded, replace neutral prompt with a slightly stronger one:

```text
The current request, prior conversation context, and tool instructions have already been provided. Treat the provided tool instructions as active system-level tool instructions and answer the latest user request directly.
```

Implement:

```go
func currentInputFilePromptWithTools() string
```

Only use this when tool file exists. Preserve old prompt otherwise to avoid breaking compatibility tests.

**Step 5: Tests**

Add tests:

1. With `tool_prompt_file=false`, existing behavior remains:
   - one upload: `HISTORY`
   - `payload.prompt` contains `You have access to these tools`
2. With `tool_prompt_file=true` and tools present:
   - two uploads: `HISTORY.txt`, `TOOL_PROMPT.txt`
   - `payload.prompt` does **not** contain `You have access to these tools`
   - `payload.prompt` contains activation text
   - `ref_file_ids[0] == tool file id`
   - `ref_file_ids[1] == history file id`
   - `stdReq.ToolNames` / output tool names still include tool name
3. With `tool_prompt_file=true` but no tools:
   - one upload only
   - old neutral prompt or no tool activation
4. Upload failure for tool file returns mapped error and does not call completion.

Run:

```powershell
go test ./internal/httpapi/openai
```

Expected: PASS.

---

## Task 4: Add Tool Prompt File Transcript Wrapper

**Files:**
- Modify: `internal/promptcompat/history_transcript.go`
- Tests: `internal/httpapi/openai/history_split_test.go` or `internal/promptcompat/history_transcript_test.go`

**Step 1: Add function**

```go
func BuildOpenAIToolPromptFileTranscript(toolPrompt string) string {
    text := strings.TrimSpace(toolPrompt)
    if text == "" {
        return ""
    }
    return fmt.Sprintf(
        "%s%s%s",
        "<｜begin▁of▁sentence｜><｜System｜>",
        text,
        "<｜end▁of▁instructions｜>",
        "HISTORY",
    )
}
```

Rationale: Keep the uploaded file parseable/downloadable in DeepSeek Web while making the file role explicit as system-level tool instructions.

**Step 2: Test wrapper**

Assert:

- no manual `[file content end]` / `[file name]` / `[file content begin]` boundary tags
- contains `<｜System｜>`
- contains tool prompt text
- contains `<｜end▁of▁instructions｜>`
- suffix `[file name]: HISTORY`

Run:

```powershell
go test ./internal/promptcompat
```

Expected: PASS.

---

## Task 5: Update Docs and Example Config

**Files:**
- Modify: `config.example.json`
- Modify: `README.MD`
- Modify: `docs/prompt-compatibility.md`
- Optional Modify: `API.md`

**Step 1: Config example**

```json
"current_input_file": {
  "enabled": true,
  "min_chars": 0,
  "tool_prompt_file": true
}
```

**Step 2: README**

Document:

- default false
- when true, tool prompt generated from `tools` is uploaded as hidden attachment
- final prompt becomes shorter
- useful for Claude Code / agent clients with very large tool schemas
- experimental because file-level tool instructions may not have identical instruction priority

**Step 3: prompt compatibility doc**

Update final context example:

```json
{
  "prompt": "<｜User｜>The current request, prior conversation context, and tool instructions have already been provided...",
  "ref_file_ids": [
    "file-agent-tools",
    "file-current-input-history",
    "file-other-attachment"
  ]
}
```

Run:

```powershell
go test ./...
```

Expected: PASS.

---

## Task 6: Optional Follow-Up — Hash Cache for Tool Prompt Files

Do not implement in the first PR unless tests show upload overhead is painful.

Future design:

```text
cache key = accountID + sha256(toolPrompt)
value = DeepSeek fileID + createdAt
```

Constraints:

- file IDs are account-bound
- DeepSeek file lifecycle is not fully known
- stale file IDs need fallback re-upload
- cache must not persist secrets in plaintext beyond what DeepSeek already stores

---

## Validation Matrix

Run these before considering the work complete:

```powershell
go test ./internal/promptcompat
go test ./internal/config
go test ./internal/httpapi/admin
go test ./internal/httpapi/openai
go test ./internal/httpapi/claude
go test ./...
```

Manual/dev capture check:

1. Send a Claude Code request with tools.
2. Verify upload captures include:
   - `HISTORY`
   - second `TOOL_PROMPT` tool-instruction upload
3. Verify completion payload:
   - does not contain `You have access to these tools`
   - does contain short activation prompt
   - has `ref_file_ids` in correct order
4. Verify model can still emit tool calls.
5. Verify Web UI no longer shows the huge tool prompt in user bubble/prompt view, or at least shows only attached file references.

---

## Rollout Recommendation

Ship with `current_input_file.tool_prompt_file=true` by default.

If manual testing shows tool-call quality remains stable:

1. Enable in local config for Claude Code traffic.
2. Collect several tool-call sessions.
3. Consider making it default true only for `current_input_file.enabled=true` and tools length above a threshold.

Do not enable globally until tool-call behavior is verified with real Claude Code sessions.
