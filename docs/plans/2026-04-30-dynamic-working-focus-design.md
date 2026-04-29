# Dynamic Working Focus Design

Date: 2026-04-30

## Goal

Fix repeated-answer and stale-attention bugs by separating completed session history from the current request's volatile working focus.

The model should:

- answer the latest user message when the last message is from `user`;
- continue only a real active assistant task when the last message is not from `user`;
- never revive completed or obsolete assistant working content;
- keep `HISTORY.txt` isolated per session so unrelated sessions cannot leak context.

## Core Principle

`working` is not memory. It is the current session's current-turn focus.

```text
┌──────────────────────────────┐
│ Stable request/config context │  model, tools, system instructions
├──────────────────────────────┤
│ Session history              │  completed context for this session only
├──────────────────────────────┤
│ Dynamic working focus         │  volatile current-turn attention
└──────────────────────────────┘
```

`HISTORY.txt` stores completed context. `working` stores only the active focus needed to finish the current turn.

## Recommended Approach

Use a small focus state machine driven by the last message role plus active working state.

Rejected alternatives:

- **Role-only routing**: `last.role == user` means answer user, otherwise continue assistant. This is too weak because tool results, assistant progress messages, and interrupted streams all have different semantics.
- **Full event sourcing**: storing every state transition and recomputing focus would be precise but overbuilt for this bug.

The chosen approach is the smallest robust change: role check plus explicit working status.

## Working Focus Model

Implementation can use this exact shape or equivalent internal fields:

```go
type WorkingFocus struct {
    SessionID       string
    FocusKind       string // "latest_user" | "assistant_pending_task"
    SourceMessageID string
    TaskID          string
    Content         string
    Status          string // "active" | "completed" | "obsolete"
    TurnIndex       int
}
```

Required invariants:

1. A working focus belongs to exactly one `sessionId`.
2. Active working must reference a source message.
3. Assistant continuation must bind to a `taskId`.
4. `completed` and `obsolete` working must never be injected into a future prompt.
5. New user input makes previous volatile assistant working obsolete.

## Focus Decision Logic

```text
                              ┌──────────────────────┐
                              │ incoming request      │
                              └──────────┬───────────┘
                                         │
                                         v
                            ┌─────────────────────────┐
                            │ inspect last message    │
                            └───────┬─────────┬───────┘
                                    │         │
                         last=user  │         │ last!=user
                                    │         │
                                    v         v
                    ┌──────────────────┐   ┌──────────────────────────┐
                    │ focus latest user │   │ active pending task?      │
                    │ old working stale │   └───────────┬──────────────┘
                    └─────────┬────────┘               │
                              │                 yes    │    no
                              │                        │
                              v                        v
                      answer latest user        continue task       no working
```

Pseudo-code:

```go
func DetermineFocus(sessionID string, messages []Message, previous WorkingFocus) WorkingFocus {
    last := LastMessage(messages)

    if last.Role == "user" {
        return WorkingFocus{
            SessionID:       sessionID,
            FocusKind:       "latest_user",
            SourceMessageID: last.ID,
            Content:         ExtractText(last),
            Status:          "active",
        }
    }

    if previous.SessionID == sessionID &&
        previous.Status == "active" &&
        previous.FocusKind == "assistant_pending_task" {
        return previous
    }

    return WorkingFocus{SessionID: sessionID, Status: "obsolete"}
}
```

## Prompt Assembly Rules

### Last message is `user`

Use:

- current session `HISTORY.txt`;
- latest user message;
- stable request/tool instructions.

Do not inject stale assistant working.

Intent:

```text
The current request and prior conversation context have already been provided.
Answer the latest user request directly.
```

### Last message is not `user`, active pending task exists

Use:

- current session `HISTORY.txt`;
- bound tool/result context;
- active working focus for the same `taskId`.

Intent:

```text
Continue the active assistant task using the provided tool/result context.
Do not repeat already completed answers.
```

### Last message is not `user`, no active pending task exists

Do not inject working. Do not revive the previous user request. Do not generate a repeated final answer from old assistant content.

## State Transitions

```text
new user message
    │
    v
┌────────────────────────┐
│ latest_user            │
│ status: active         │
└───────────┬────────────┘
            │ assistant starts tool call / partial work
            v
┌────────────────────────┐
│ assistant_pending_task │
│ status: active         │
└───────────┬────────────┘
            │ final answer completed
            v
┌────────────────────────┐
│ completed              │
│ never inject again     │
└────────────────────────┘
```

Any new user message makes previous volatile working obsolete unless the user explicitly asks to continue it. Even then, the latest user message remains the focus and history is only used to resolve the reference.

## Session Isolation

All history and working paths must be scoped by sanitized `sessionId`.

Recommended layout:

```text
data/sessions/{sessionID}/HISTORY.txt
data/sessions/{sessionID}/working.json
```

Rules:

- never read global `HISTORY.txt`;
- never read another session's working file;
- reject empty or path-traversal session IDs;
- write via temp file then atomic rename;
- use a per-session lock or equivalent to avoid concurrent request races.

## Tool and Stream Handling

Tool continuation must bind to the same active task:

```text
user message
  ↓ creates taskId
assistant tool call
  ↓ carries taskId
tool result
  ↓ updates same taskId
assistant final answer
  ↓ marks task completed
```

Completion rules:

- `finish_reason == stop`: mark related working `completed`;
- stream interruption, unfinished tool call, or retryable upstream failure: keep working `active`;
- new user message: mark old active assistant task `obsolete` and focus on the new user request.

## Short Follow-up Handling

Messages like "继续", "那怎么办", or "修一下" are still latest user messages.

The focus is the new user message, but session history and summary may be used to resolve what the short phrase refers to. This avoids both stale working resurrection and context loss.

## Tests

### 1. Latest user wins

```text
user: A?
assistant: answer A
user: B?
assistant: answer B
```

Assert that the assembled prompt focuses on B and does not inject completed working for A.

### 2. Completed answer is not revived

```text
user: A?
assistant: answer A
next request has no new user and no pending task
```

Assert that no active working is injected and A is not repeated.

### 3. Tool result continues the pending task

```text
user: inspect file
assistant: tool call
tool: result
assistant: final answer
```

Assert that the tool result binds to the same `taskId`, the task is continued once, and working becomes `completed`.

### 4. New user interrupts old pending task

```text
user: do A
assistant: starts A
user: ignore that, do B
```

Assert that A working becomes `obsolete` and B becomes `latest_user`.

### 5. Session isolation

```text
session-1: asks A
session-2: asks B
```

Assert that session-2 `HISTORY.txt` and working context contain no session-1 content.

### 6. Short follow-up

```text
user: explain this bug
assistant: explains
user: continue
```

Assert that focus is the latest user message and history is used only to resolve the reference.

### 7. Interrupted stream recovery

```text
user: do A
assistant stream interrupts before final answer
next request resumes
```

Assert that working stays `active` and is not marked `completed`.

## Not In Scope

- Rewriting the entire history system.
- Full event sourcing or replay UI.
- Cross-session long-term memory.
- UI changes except optional debug display.
- Changing the external tool protocol beyond internal task binding.

## Success Criteria

- Latest user input always has highest priority.
- Completed and obsolete working are never injected.
- Tool results continue only the matching task.
- `HISTORY.txt` is strictly session-scoped.
- Short follow-ups retain enough context without reviving stale working.
- Interrupted tasks can recover without being misclassified as complete.
- Regression tests cover repeated-answer, tool-chain, session-isolation, interruption, and short-follow-up cases.
