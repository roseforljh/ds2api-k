# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build / Lint / Test

```bash
# Build the main binary
go build -o ds2api ./cmd/ds2api

# Build admin WebUI (Node.js required, outputs to static/admin/)
./scripts/build-webui.sh

# Cross-compile release archives for all platforms → dist/
./scripts/build-release-archives.sh

# Lint (golangci-lint v2, auto-bootstraps if missing)
./scripts/lint.sh

# Run all unit tests (Go + Node)
./tests/scripts/run-unit-all.sh

# Run only Go unit tests
go test ./...

# Run a single Go package test
go test ./internal/sse/ -run TestDedupe -v

# Run raw SSE stream regression tests
./tests/scripts/run-raw-stream-sim.sh

# Run a single raw stream sample
node tests/tools/deepseek-sse-simulator.mjs --samples-root tests/raw_stream_samples --sample-id <id>
```

## Architecture

ds2api is a DeepSeek API proxy that exposes OpenAI, Claude, and Gemini-compatible endpoints. It logs into real DeepSeek user accounts, manages session tokens and PoW challenges, then translates standard SDK requests into DeepSeek's internal protocol.

### Request flow for `/v1/chat/completions`

```
Auth (API key → direct or pooled account) → Parse JSON → Preprocess inline files
→ PromptCompat normalization → CurrentInputFile injection → CreateSession
→ GetPow → CallCompletion (SSE) → SSE parse (thinking/text/tool-call detection)
→ Format OpenAI response → Stream back to client
```

### Key layers

- **`internal/server/router.go`** — chi router with all public routes. Every handler is wired here with its dependencies (Store, Auth resolver, DS client, ChatHistory). CORS, logging, healthz/readyz are middleware.
- **`internal/config/`** — `Store` wraps a `Config` struct with `sync.RWMutex`. Loads from `config.json` or `DS2API_CONFIG_JSON` env var. Thread-safe reads/writes with index rebuilds. Paths resolve via `ResolvePath(envKey, defaultRel)`.
- **`internal/auth/request.go`** — `Resolver.Determine(req)` is the auth gate for every API call. If the caller's Bearer token is in `config.keys`, it enters managed mode: acquires an account from the pool, logs in (or reuses cached token), and injects `RequestAuth` into context. Otherwise the token is passed through directly to DeepSeek. Token refresh and account switching happen automatically on auth failures.
- **`internal/account/pool_core.go`** — `Pool` maintains a FIFO queue of account identifiers. `AcquireWait` blocks with a waiter channel when all accounts are at their per-account concurrency limit (default 2). A released slot notifies the next waiter. `bumpQueue` rotates a just-used account to the back.
- **`internal/deepseek/client/`** — `Client` handles Login (email/mobile + password → token), CreateSession, GetPow (PoW challenge via `pow/` package), and CallCompletion (streaming SSE POST). Retries with token refresh and account switching. Proxy support via `requestClientsForAccount` which builds per-account HTTP clients from `config.Proxies`.
- **`internal/httpapi/openai/chat/handler_chat.go`** — `ChatCompletions` is the main handler. Orchestrates the full flow, delegates streaming to `handleStream` / `handleNonStream` with empty-output retry logic. Non-stream path collects all SSE chunks via `sse.CollectStream` then formats a single JSON response.
- **`internal/httpapi/openai/chat/chat_stream_runtime.go`** — SSE-to-OpenAI-chunk translation. Tracks thinking vs text state, buffers tool call content, emits incremental tool call deltas, and handles citation link replacement for search results.
- **`internal/sse/parser.go`** — Low-level SSE line parser with deduplication for continue-thinking sequences.
- **`internal/stream/`** — Generic SSE consumption engine with keepalive, idle timeout, and context cancellation handling. Used by the chat handler's `ConsumeSSE`.
- **`internal/config/models.go`** — Model alias resolver. Maps 100+ model names (GPT-4/5, Claude 3/4, Gemini 1.5/2.x/3.x, Llama, Qwen) to 6 DeepSeek base models. Each model also has a `-nothinking` variant. Unknown model prefixes are rejected.
- **`internal/format/openai/`** — Builds OpenAI-compatible chat completion response JSON including tool call formatting and usage calculation.
- **`internal/webui/`** — Serves the embedded admin SPA from `static/admin/`. `EnsureBuiltOnStartup` checks for the built assets and logs a warning if missing.

### Admin API (`/admin/*`)

JWT-secured (HS256, env `DS2API_ADMIN_KEY` or `config.admin.password_hash`). Sub-routers: accounts CRUD and testing, config import/export, proxy management, runtime settings, raw SSE sample capture, chat history browsing, dev capture, Vercel sync, version info.

### PoW (`pow/`)

Pure Go implementation of DeepSeekHashV1 (SHA3-256 Keccak-f[1600] skipping round 0, only rounds 1..23). `SolvePow` brute-forces nonces to match the server challenge. Called from `internal/deepseek/client/pow.go` which builds the `x-ds-pow-response` header.

### Deployment targets

- **Standalone binary**: `cmd/ds2api/main.go` — listens on `0.0.0.0:$PORT` (default 5001), graceful shutdown on SIGTERM/SIGINT
- **Vercel serverless**: `api/index.go` — uses `sync.Once` singleton, config via `DS2API_CONFIG_JSON` env var
- **Docker**: `Dockerfile` exists, env-driven config via `.env.example`

### Configuration

`config.json` schema fields (see `internal/config/config.go` `Config` struct): `keys`, `api_keys`, `accounts` (email/mobile + password), `proxies` (HTTP/SOCKS5), `model_aliases`, `admin` (password_hash, JWT settings), `runtime` (concurrency limits, token refresh interval), `compat`, `auto_delete`, `history_split`, `current_input_file`, `thinking_injection`.
