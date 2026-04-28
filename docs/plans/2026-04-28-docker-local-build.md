# Docker Local Build Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make local Docker Compose build and run the latest code from the current repository instead of pulling the original author's published image.

**Architecture:** Reuse the existing multi-stage Dockerfile and switch the main compose service to a local build-based workflow. Remove the extra development compose file so the repository has one Docker Compose path for local secondary development, then align the docs to that workflow.

**Tech Stack:** Docker Compose, multi-stage Dockerfile, Markdown documentation

---

### Task 1: Switch the main Compose service to local source builds

**Files:**
- Modify: `C:/Users/33039/Desktop/ds2api-k/docker-compose.yml`

**Step 1: Write the failing check**

Verify the current compose file still references the upstream image instead of a local build.

**Step 2: Run check to verify current behavior**

Run: `rg -n 'ghcr.io/cjackhwang/ds2api:latest|build:' 'C:/Users/33039/Desktop/ds2api-k/docker-compose.yml'`
Expected: Match for the upstream image and no local build stanza.

**Step 3: Write minimal implementation**

Replace the upstream `image:` reference with a `build:` block using the repository root as context and the Dockerfile's final runtime target. Keep the existing env file, ports, volumes, and environment settings intact. Optionally keep a local image tag such as `ds2api:local` if helpful for local reuse, but do not reference the upstream registry.

**Step 4: Run validation**

Run: `docker compose -f 'C:/Users/33039/Desktop/ds2api-k/docker-compose.yml' config`
Expected: Compose renders successfully and shows the service is build-based.

**Step 5: Commit**

```bash
git add docker-compose.yml
git commit -m "refactor: build docker compose from local source"
```

### Task 2: Remove the redundant dev compose entrypoint

**Files:**
- Delete: `C:/Users/33039/Desktop/ds2api-k/docker-compose.dev.yml`

**Step 1: Write the failing check**

Confirm the dev compose file still exists and represents a second Docker workflow.

**Step 2: Run check to verify current state**

Run: `python -c "from pathlib import Path; print(Path(r'C:/Users/33039/Desktop/ds2api-k/docker-compose.dev.yml').exists())"`
Expected: `True`

**Step 3: Write minimal implementation**

Delete `docker-compose.dev.yml`.

**Step 4: Run validation**

Run: `python -c "from pathlib import Path; print(Path(r'C:/Users/33039/Desktop/ds2api-k/docker-compose.dev.yml').exists())"`
Expected: `False`

**Step 5: Commit**

```bash
git add -u docker-compose.dev.yml
git commit -m "chore: remove redundant docker dev compose"
```

### Task 3: Update Docker documentation for local secondary development

**Files:**
- Modify: `C:/Users/33039/Desktop/ds2api-k/README.MD`
- Modify: `C:/Users/33039/Desktop/ds2api-k/README.en.md`
- Modify: `C:/Users/33039/Desktop/ds2api-k/docs/DEPLOY.md`
- Modify: `C:/Users/33039/Desktop/ds2api-k/docs/DEPLOY.en.md`

**Step 1: Write the failing check**

Find documentation that still describes Docker deployment primarily as pulling a published image or omits the required local build command.

**Step 2: Run check to verify current wording**

Run: `rg -n 'ghcr.io/cjackhwang/ds2api:latest|docker-compose.dev.yml|docker-compose up -d|Docker / GHCR ????|Docker / GHCR image deployment' 'C:/Users/33039/Desktop/ds2api-k/README.MD' 'C:/Users/33039/Desktop/ds2api-k/README.en.md' 'C:/Users/33039/Desktop/ds2api-k/docs/DEPLOY.md' 'C:/Users/33039/Desktop/ds2api-k/docs/DEPLOY.en.md'`
Expected: Existing wording that needs alignment with the new local-build-first workflow.

**Step 3: Write minimal implementation**

Update all affected docs to state that local Docker Compose now builds the current repository code, recommend `docker compose up -d --build`, and remove references to the deleted dev compose file where applicable.

**Step 4: Run validation**

Run the same `rg` command from Step 2 and manually inspect the changed sections for correct wording.
Expected: No stale guidance about the upstream image as the default local compose path or about the removed dev compose file.

**Step 5: Commit**

```bash
git add README.MD README.en.md docs/DEPLOY.md docs/DEPLOY.en.md
git commit -m "docs: update docker local build guidance"
```
