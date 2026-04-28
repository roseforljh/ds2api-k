# Docker Local Build Design

**Problem**
The current local Docker deployment uses the upstream published image `ghcr.io/cjackhwang/ds2api:latest`, so `docker compose up` does not run the latest local source changes. This is unsuitable for secondary development.

**Decision**
Adopt a single local Docker deployment entrypoint that always builds from the current repository source. Remove the separate development compose file to avoid two competing workflows.

## Scope
- Change `docker-compose.yml` to build from the local repository instead of pulling the upstream image.
- Delete `docker-compose.dev.yml`.
- Update Docker deployment docs to describe the new local-source build workflow.

## Chosen Approach
Use the existing multi-stage `Dockerfile` and make `docker-compose.yml` build the `final` target locally. Keep runtime behavior unchanged where possible: same env file, same port mapping, same config bind mount, same container name.

## Alternatives Considered
1. Keep the upstream image for production and add a second local compose file. Rejected because the user wants one default path for local secondary development.
2. Keep both `image` and `build` in the main compose. Rejected because it preserves unnecessary ambiguity about source of truth.

## Expected Result
After the change, `docker compose up -d --build` will compile and run the latest checked-out code in this repository, making Docker the default workflow for local customization and testing.
