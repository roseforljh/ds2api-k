# Repository Guidelines

## Project Structure & Module Organization

This Go 1.26 service includes a React admin UI. Entry points are `cmd/ds2api`, `cmd/ds2api-tests`, and `cmd/pow_solver`. Core packages live under `internal/`: HTTP APIs in `internal/httpapi`, DeepSeek client code in `internal/deepseek`, config/auth/account logic in `internal/config`, `internal/auth`, and `internal/account`, plus stream/tool parsing in `internal/sse`, `internal/toolcall`, and `internal/toolstream`. Serverless entry files are in `api/`. Frontend code is in `webui/src`.

## Build, Test, and Development Commands

- `go test ./...`: run all Go package tests.
- `go build ./cmd/ds2api`: compile the main service binary.
- `./scripts/lint.sh`: run repository lint checks.
- `./tests/scripts/run-unit-all.sh`: run the full unit test suite.
- `npm run build --prefix webui`: build the admin UI assets.
- `cd webui; npm run dev`: start the Vite dev server.

## Coding Style & Naming Conventions

Format changed Go files with `gofmt`; lint configuration lives in `.golangci.yml`. Keep package names short, lowercase, and aligned with directory names. Do not ignore cleanup errors from `Close`, `Flush`, `Sync`, or similar methods; return or log them. Follow patterns such as `handler_*.go`, `*_runtime.go`, and `*_route_test.go`. Frontend code uses React JSX, feature folders under `webui/src/features`, and PascalCase component filenames.

## Testing Guidelines

Add or update tests next to the affected package. Prefer focused regression tests for stream parsing, tool calls, HTTP response shapes, and config behavior. Run `go test ./...` for Go changes, `npm run build --prefix webui` for UI changes, and gates before handoff.

## Commit & Pull Request Guidelines

Recent commits are short and imperative, often numeric or Chinese summaries such as `修复前端中断后端还在跑的bug`. Keep commits focused; do not mix unrelated refactors into feature work. Pull requests should describe the change, list validations, link issues when available, and include screenshots only for UI changes.

## Agent-Specific Instructions

Keep changes scoped to the requested feature or bugfix. When business logic or user-visible behavior changes, update matching docs in the same change. `docs/prompt-compatibility.md` is the source of truth for API-to-web-chat compatibility; update it when changing message normalization, tool prompt injection, file/reference handling, history split, or completion payload assembly.

## Security & Configuration Tips

Do not commit real secrets from `.env` or `config.json`; use `.env.example` and `config.example.json` as templates. Mask API keys in logs, tests, screenshots, and UI output.
