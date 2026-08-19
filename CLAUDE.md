# claumon

Real-time Claude Code dashboard — single binary, zero config. Monitors API usage, token costs, sessions, and memory files via a local web UI on port 3131.

## Build & Test

```bash
make build            # builds ./claumon with version from git tags
make build-benchtools # build including the dev-only bench/diagnostics subcommands
make test             # go test -v -race -count=1 ./...
make vet              # go vet ./...
./claumon --open      # run and open browser
```

The `bench` and `diagnostics` subcommands (forecast model benchmarking and
calibration replay) are gated behind the `benchtools` build tag, so they are
excluded from release binaries. Use `make build-benchtools` (or
`go build -tags benchtools .`) to enable them locally.

## Architecture

Single `main.go` orchestrates 4 goroutines (SSE broker, usage API poller, file watcher, pricing refresh) and an HTTP server. All packages live under `internal/`:

- **api** — Claude OAuth usage API client with exponential backoff
- **auth** — Multi-platform credential loading (macOS Keychain, Linux secret-service, Windows Credential Manager)
- **parser** — Session JSONL discovery and token/cost aggregation
- **pricing** — Layered pricing: embedded JSON → 24h cache → GitHub remote → config overrides
- **memory** — Memory file discovery, graph building, staleness detection, consolidation scoring
- **server** — HTTP routes, handlers, SSE broker
- **store** — SQLite (WAL mode) for usage snapshots and daily aggregates
- **watcher** — fsnotify-based file watcher with 500ms debounce

Frontend is a single `web/index.html` embedded via `//go:embed`. No build step, no external JS deps.

`extension/` is a separate VS Code extension (TypeScript): a thin client that reads the same `/api/usage` + SSE feed and renders usage/forecast in the status bar plus an embedded dashboard panel. It has its own Node toolchain, version, and lockfile, and **zero runtime dependencies** (compiled with `tsc`, not bundled). It is not part of the Go build and is published separately (see the `ext-v*` tag in `.github/workflows/publish-extension.yml`).

## Conventions

- Log format: `log.Printf("[tag] message", ...)` with tags like `[poll]`, `[watcher]`, `[memory]`, `[auth]`, `[backfill]`, `[aggregate]`
- Errors: return errors, don't panic. Fatal only in `main()` for startup failures (DB open).
- JSON API responses always use `writeJSON`/`writeJSONError` helpers. Return empty slices (not null) for empty collections.
- Tests: table-driven where applicable, `_test.go` alongside source files. Race detector is on.
- Version injected via `-ldflags "-X main.version=..."` at build time.
- Cross-platform: no CGO (`CGO_ENABLED=0`), builds for darwin/linux/windows × amd64/arm64 via goreleaser.

## Git hygiene — do not create orphans

An audit on 2026-08-18 found **21 abandoned stashes** (oldest 253 days) and **8 orphaned worktrees**
holding 103 uncommitted files. Most were created by parallel agents stashing to switch branches and
never restoring. This is the single most expensive habit in this workspace.

- **Prefer a WIP commit on a branch over `git stash`.** A commit has a name, a branch, and shows up
  in `git log`. A stash is invisible until someone runs `git stash list`, and nobody does.
  If you must stash, give it a real message (`git stash push -m "why, and what to do next"`) — never
  a bare `git stash`.
- **A worktree belongs to a task.** When the task ends, `git worktree remove` it. Do not leave one
  behind "in case" — that is how 19 GB accumulated.
- **Never stash another agent's changes.** In a shared tree, a stash silently removes someone else's
  uncommitted work from their view. Stop and ask instead.
- **Before finishing a session that touched branches**, run `git-hygiene .` and leave the repo no
  worse than you found it.

`git-hygiene` (in `~/.local/bin`) reports stale stashes and dirty or already-merged worktrees.
A SessionStart hook warns automatically when the current repo has either.

## Task capture (planned)

Follow-up work found here should become an **aven task**, not prose that scrolls away — deferred
scope, blocked items, gaps that need a human decision or an external lookup. Not built yet; the
hook points are recorded in `~/.claude/AVEN-INTEGRATION.md`. A fork of aven is intended.
