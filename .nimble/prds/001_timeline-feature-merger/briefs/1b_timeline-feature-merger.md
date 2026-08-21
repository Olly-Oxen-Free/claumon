# Task Brief: 1b

**Title:** NIMBLE PRD panel (new tab reading local `.nimble/prds/*` state)
**PRD:** timeline-feature-merger
**Priority:** should
**Complexity:** 5/10
**Model:** opus
**Wave:** 1
**Feature Issue:** TBD — `status.json.tracker.issue_key` is `null` for this PRD; no GitHub issue has been created yet. If the PM agent has since created one, use that number in place of this placeholder.

---

## Objective

Add a new "NIMBLE" tab to claumon's dashboard that reads local `.nimble/prds/*/{tasks.yaml,execution_plan.yaml,status.json}` files directly off disk and renders each discovered PRD's status, wave progress, and any BLOCKED/STALE/CRASHED alerts. This closes the gap where NIMBLE-orchestrated PRD execution is visible only via the `/nimble:dashboard` CLI command, forcing a context switch away from the dashboard the user is already watching.

---

## Context

**Parent Feature:** Timeline & Event Viewer Feature Merger (feature-branch execution: `feature/timeline-feature-merger`, PR target `origin` fork `Olly-Oxen-Free/claumon`, not the `fabioconcina/claumon` upstream — see `.nimble/config.yaml:16-20`).

claumon is a single-binary, zero-build-step, pull-based dashboard for Claude Code sessions. This PRD merges the highest-value patterns from a comparative UX study of three systems (claumon itself, the `agents-observe` plugin, and NIMBLE/karimo's `/nimble:dashboard`) into claumon's existing Timeline tab and, for this task specifically, a brand-new standalone tab. Unlike the other five tasks in this PRD, 1b has **no coupling to the Timeline tab's gantt or event-list code at all** — it is a pure file-format read of NIMBLE's own on-disk PRD state, rendered as a new top-level dashboard panel.

`/nimble:dashboard` (System C in the research) is a point-in-time CLI text report that re-derives the same state from `status.json` + `tasks.yaml` + `execution_plan.yaml` on each invocation, with no live/streaming mechanism of its own. This task is a *data-format integration*, not a live-protocol integration: read the same files directly, on-demand, over HTTP, the same way claumon already reads `~/.claude/projects/**/*.jsonl` live per request with no SQLite involved.

This task is part of **Wave 1** — three tasks (1a, 1b, 2a) run with no dependencies on each other and can execute fully in parallel. 1b has no downstream dependents (2b depends on 1a and 2a, not 1b).

---

## Research Context

### Patterns to Follow

- **Project/CWD discovery — reuse, don't invent:** `internal/timeline/fleet.go:202` calls `parser.DiscoverRecentSessions(claudeDir, scanLimit)` to get `[]*parser.SessionSummary`, each with a `CWD` field (`internal/parser/sessions.go:41`). This is the *same source* the task description points to ("same source fleet.go already uses") — call it the same way rather than writing a new session-scanning mechanism.
- **"No projects directory" is an empty result, not an error:** `internal/timeline/fleet.go:202-211` treats `os.ErrNotExist` from `DiscoverRecentSessions` as an empty result (`return &Fleet{..., Sessions: []FleetSession{}}, nil`), specifically to avoid a 500 on a first-run install with no `~/.claude/projects` directory yet. Mirror this: a machine/repo with no `.nimble/` anywhere is an empty panel, not an error.
- **One-package-per-subsystem convention:** `internal/pricing/pricing.go` and `internal/parser/sessions.go` are the reference shape — `package {name}` at the top, a small set of exported types (e.g. `pricing.ModelPricing`, `pricing.Table`) and exported functions (`DiscoverSessions`, `DiscoverRecentSessions`), doc comments on every exported symbol, no `internal/` sub-splitting for a subsystem this size. `internal/nimble` should follow the same shape: one or two files, package `nimble`, exported types like `nimble.PRD` / `nimble.Task` / `nimble.Alert`, and an exported `Discover(claudeDir string) ([]nimble.ProjectPRDs, error)`-style entry point.
- **Handler registration pattern:** every route in `internal/server/server.go` is a one-line `mux.HandleFunc("GET /api/...", handlers.Handle...)` call (lines 25-58), and every handler is a method on `*Handlers` in `internal/server/handlers.go` (e.g. `HandleFleet`, `internal/server/handlers.go:659-688`) that reads query params, calls into the relevant `internal/*` package, and calls `writeJSON`/`writeJSONError`. Follow this exactly: add `HandleNimble` to `internal/server/handlers.go`, register `mux.HandleFunc("GET /api/nimble/prds", handlers.HandleNimble)` in `internal/server/server.go`.
- **`Handlers` already has `claudeDir`:** `internal/server/handlers.go:26` (`claudeDir string`, set in `NewHandlers`, `internal/server/handlers.go:78-85`). The new handler needs no new field — call `parser.DiscoverRecentSessions(h.claudeDir, ...)` directly from the handler or from inside `internal/nimble` (your call; `fleet.go` takes `claudeDir` as a parameter into the package rather than reaching into `Handlers`, which is the more testable shape — prefer that).
- **Tab wiring is three small, disjoint edits, not one:**
  1. Nav button — add a `<div class="tab" data-tab="nimble">NIMBLE</div>` alongside the existing four at `web/index.html:1380-1383`.
  2. Tab-content panel — add a new `<div class="tab-content" id="tab-nimble">...</div>` block. Insert it as a sibling of the existing four tab-content divs, immediately after the Timeline tab's closing `</div>` at `web/index.html:1563` and before the tab-content container's own closing `</div>` at `web/index.html:1564`.
  3. Activation wiring, two places:
     - Click handler (`web/index.html:1591-1601`) — this loop is generic (`document.getElementById('tab-' + tab.dataset.tab)`) and needs **no edit** for the panel to become visible, but if the panel's data should be fetched lazily (recommended — see Implementation Guidance) add one line following the existing `if (tab.dataset.tab === 'timeline') tlActivate();` pattern at line 1599, e.g. `if (tab.dataset.tab === 'nimble') nimbleActivate();`.
     - Keyboard 1-4 shortcut handler (`web/index.html:2244-2254`) — this is the "activation switch" the task description and PRD both name explicitly. Extend `const tabs = ['dashboard', 'memories', 'graph', 'timeline'];` (line 2244) to `['dashboard', 'memories', 'graph', 'timeline', 'nimble']` and `const tabIndex = { '1': 0, '2': 1, '3': 2, '4': 3 }[e.key];` (line 2245) to add `'5': 4`. Add a mirrored `if (tabs[tabIndex] === 'nimble') nimbleActivate();` alongside the existing lines 2251-2252 if you added the lazy-fetch pattern above.

### Known Issues to Address

- **`status.json` schema is under-documented in this repo.** This PRD's own `.nimble/prds/001_timeline-feature-merger/status.json` has `"tasks": {}` (empty — no task has run yet), so it is *not* a usable example of a populated per-task status entry. The only schema information available is from `research/findings.md`'s System C section: task status enum is `queued → running → in-review → done`, plus `needs-revision`, `needs-human-review`, `failed`, `crashed`, `blocked`, `paused`, `awaiting-human`. No JSON key names for how these are nested per-task are confirmed anywhere in this repo. **Parse defensively**: treat unknown/missing status fields as "unknown" rather than crashing or silently treating them as healthy, and do not assume a specific nesting shape beyond what you can verify by reading a real `status.json` if one becomes available in a NIMBLE installation on the machine (e.g. another repo's `.nimble/prds/*/status.json`, if any exist elsewhere on this machine — check before assuming the shape).
- **`tasks.yaml` uses block scalars** (see this PRD's own `tasks.yaml`, e.g. the `description: >` fields at lines 10-19, 33-47) — this is exactly why the task spec calls for `gopkg.in/yaml.v3` instead of a hand-rolled parser. Do not attempt a regex/line-based parser; unmarshal into Go structs via `yaml.v3`.
- **No single "current repo" concept.** claumon may be watching Claude Code sessions across many unrelated project directories. The panel's data model must be a list grouped by project (CWD), each project potentially containing zero or more `.nimble/prds/*` entries — never assume exactly one project or one PRD.

### Recommended Approach

- **New dependency:** `gopkg.in/yaml.v3`, pure Go, no CGO. Add via `go get gopkg.in/yaml.v3` (do not hand-edit `go.mod`/`go.sum`) so the toolchain resolves the correct version and checksums. Confirmed pure-Go/CGO-free per PRD research and the existing `go.mod` (all four current direct deps — `fsnotify`, `goldmark`, `golang.org/x/sys`, `modernc.org/sqlite`, plus `modernc.org/*` indirects — already build under `CGO_ENABLED=0`, per `.goreleaser.yml:8` and this repo's cross-compile release process; `yaml.v3` is well-established as CGO-free and will not change that).
- **Discovery algorithm:**
  1. Call `parser.DiscoverRecentSessions(claudeDir, limit)` (reuse `fleet.go`'s pattern; a limit of 500 matches `HandleFleet`'s default, `internal/server/handlers.go:672`).
  2. Collect the unique, non-empty `CWD` values across the returned `[]*parser.SessionSummary`.
  3. For each unique CWD, check for a `.nimble/prds/` directory (`os.Stat` or `filepath.Glob(filepath.Join(cwd, ".nimble/prds/*"))`).
  4. For each PRD directory found (e.g. `NNN_slug/`), read `tasks.yaml`, `execution_plan.yaml`, and `status.json` if present; each is independently optional (a PRD mid-`/nimble:plan` may have `tasks.yaml` but no `status.json` yet — do not fail the whole PRD entry if one file is missing, per this PRD's own metadata frontmatter `status: "draft"` for an example of a PRD that predates full task/status generation).
  5. Group the result by project (CWD), each project holding zero or more PRD summaries.
- **BLOCKED/STALE/CRASHED surfacing:** cross-reference `status.json`'s task states (however they are actually nested — verify by reading a real file if one can be found; do not guess a nested shape you cannot confirm) against the enum in Research Context above; treat `blocked`, `crashed`, and any state whose `updated_at`/equivalent timestamp is stale beyond a reasonable threshold (no fixed number given in this PRD — pick something defensible like 24h and document the choice inline in a comment) as an alert.

### Dependencies

**File Dependencies:**
- `internal/parser/sessions.go` — `parser.SessionSummary.CWD` (line 41) and `parser.DiscoverRecentSessions` (lines 349-362) are read-only reuse, not modified by this task.
- `internal/timeline/fleet.go:202-211` — read as a reference pattern for the "missing directory is empty, not an error" idiom; not modified by this task.

**Library Dependencies:**
- `gopkg.in/yaml.v3` — new, added via `go get`, must remain the *only* new dependency this PRD introduces (per PRD §6 Internal Dependencies and §11 Architecture Decisions).

---

## Requirements

Implements **R4** from the PRD (`Should Have`):

> New tab/panel lists local `.nimble/prds/*` folders with status, wave progress, and any BLOCKED/STALE alerts, read directly from `tasks.yaml`/`execution_plan.yaml`/`status.json`.

Scope is explicitly capped (PRD §6, risk mitigation row): status/wave/alerts display only. **Do not** implement a health-score formula, velocity/resource-usage sections, or anything else from `/nimble:dashboard`'s CLI output beyond what R4's acceptance criteria name — that CLI feature is out of scope for this task, mentioned only as prior art.

1. **Discovery** — enumerate unique project directories from `parser.SessionSummary.CWD` across recent sessions (reuse `parser.DiscoverRecentSessions`, do not write a new session-scan mechanism); scan each project directory for a `.nimble/prds/` folder; group findings by project.
2. **Parsing** — for each discovered PRD directory, parse `tasks.yaml`, `execution_plan.yaml`, and `status.json` (each independently optional) via `gopkg.in/yaml.v3` (YAML files) and `encoding/json` (status.json).
3. **API** — a new `GET` route on `internal/server/server.go`'s mux, backed by a new handler method on `*Handlers` in `internal/server/handlers.go`, returning the grouped-by-project PRD summaries as JSON via `writeJSON`.
4. **UI** — a new top-level "NIMBLE" tab in `web/index.html`: nav button, tab-content panel, wiring into both the click-based and keyboard-based tab-activation mechanisms.
5. **Alerts** — BLOCKED/STALE/CRASHED task states surfaced as visible per-PRD alerts in the panel.
6. **Empty state** — "No NIMBLE PRDs found" when no known project CWD has a populated `.nimble/prds/` folder.

---

## Success Criteria

Complete ALL criteria before marking task done:

- [ ] New tab lists every `.nimble/prds/*` PRD found across all known project CWDs, grouped by project, with `prd_slug`/status/wave progress
- [ ] BLOCKED/STALE/CRASHED task states (from `status.json`) surface as visible alerts on the relevant PRD
- [ ] Empty state: "No NIMBLE PRDs found" when no known project CWD has a `.nimble/prds/` folder
- [ ] `go.mod` gains `gopkg.in/yaml.v3` as the only new dependency; `CGO_ENABLED=0` build still succeeds (verify with `CGO_ENABLED=0 go build .` or `make build`, and ideally the same cross-target check goreleaser performs — see `.goreleaser.yml:8`)
- [ ] `make test` and `go vet ./...` pass

**All criteria must pass before task is complete.**

---

## Deliverables

| Artifact | Path | Done when |
|---|---|---|
| `internal/nimble` package | `internal/nimble/nimble.go` (plus any additional file(s) if you split types from discovery logic) | Exports discovery/parsing functions and types per the "one-package-per-subsystem" convention; has its own `_test.go` covering at least: a project with no `.nimble/` dir, a project with an empty `.nimble/prds/`, and a project with a populated PRD directory (this PRD's own `.nimble/prds/001_timeline-feature-merger/` is a real, on-disk fixture you can point a test at directly — note its `status.json` has `"tasks": {}`, so it exercises the "no populated task states" path, not the alert path; construct a second fixture under `testdata/` or similar for the alert path) |
| New route | `internal/server/server.go` + `internal/server/handlers.go` | `GET /api/nimble/prds` (or equivalent path — name it consistently with the rest of the API, which is uniformly `/api/{noun}` or `/api/{noun}/{sub}`) registered and wired to a new `HandleNimble`-style method returning JSON via `writeJSON` |
| NIMBLE tab | `web/index.html` | Nav button, tab-content panel, and both activation-switch edits present; panel renders grouped PRD list, per-PRD alerts, and the empty state |
| `go.mod`/`go.sum` update | `go.mod`, `go.sum` | `gopkg.in/yaml.v3` added via `go get` (not hand-edited); no other new dependency introduced |

---

## Files to Modify

| File | Action | Purpose |
|------|--------|---------|
| `internal/nimble/nimble.go` | create | New package: discover project CWDs via `parser.DiscoverRecentSessions`, scan for `.nimble/prds/*`, parse `tasks.yaml`/`execution_plan.yaml`/`status.json`, group by project, derive BLOCKED/STALE/CRASHED alerts |
| `internal/nimble/nimble_test.go` | create | Table-driven tests covering no-`.nimble` dir, empty `.nimble/prds/`, and a populated PRD (this repo's own `001_timeline-feature-merger` PRD folder is a usable read-only fixture for the "no alerts" path) |
| `internal/server/handlers.go` | modify | Add `HandleNimble` (or similarly named) method on `*Handlers`, following the `HandleFleet` shape (`internal/server/handlers.go:659-688`): call into `internal/nimble`, `writeJSON`/`writeJSONError` the result |
| `internal/server/server.go` | modify | Register the new route with `mux.HandleFunc("GET /api/nimble/...", handlers.HandleNimble)` alongside the existing routes (`internal/server/server.go:25-58`) |
| `web/index.html` | modify | Add nav button (near line 1380-1383), new `#tab-nimble` tab-content block (inserted after line 1563, before line 1564), and activation wiring in the click handler (near lines 1591-1601, optional lazy-fetch line) and the keyboard handler (`tabs` array + index map at line 2244-2245, plus the `if (tabs[tabIndex] === ...)` block at lines 2251-2252) |
| `go.mod` / `go.sum` | modify | Add `gopkg.in/yaml.v3` via `go get gopkg.in/yaml.v3` |

### File Ownership Notes

`web/index.html` is being edited concurrently by task **1a** (client-side event-list filter/search) in the same wave, with no dependency between the two tasks. The PRD explicitly scopes this to avoid collision (PRD §6 risk table, §5 UX notes): 1a's edits are confined to the event-list filter box and `tlRenderEvents`/`tlEventRow` region (inside `#tab-timeline`, around `web/index.html:1544-1563` and the corresponding `tlRenderEvents` JS function further down the file); 1b's edits are confined to the tab nav row (~1380-1383), a brand-new `#tab-nimble` block, and the two activation-switch locations (~1591-1601, ~2244-2254). **Do not touch anything inside `#tab-timeline` or any `tl*`-prefixed function/variable.** If a merge conflict does occur despite the disjoint regions, resolve by keeping both edits — the regions genuinely do not overlap in current line numbers, so a conflict would indicate one task drifted outside its scope.

---

## Implementation Guidance

### Patterns to Follow

See **Research Context** above — it is the authoritative, file:line-anchored pattern list for this task (discovery reuse, error-as-empty idiom, package shape, handler/route pattern, and the exact three-part tab-wiring edit). Do not re-derive these from scratch; the anchors were verified against the live `feature/timeline-feature-merger` branch.

### Code Style

- Go: match `internal/parser` and `internal/pricing` — doc comments on all exported identifiers, `errors.Is`/`errors.As` for sentinel/wrapped-error checks (see `fleet.go:207`'s `errors.Is(err, os.ErrNotExist)`), no panics outside `main()`.
- Frontend: match the existing vanilla-JS style in `web/index.html` — no new build step, no framework, function names that share the tab's short prefix the way `tl*` (timeline) and `fl*` (fleet gantt) do; `nimble*` or `nb*` is a reasonable choice (`nimbleActivate`, `nimbleRender`, etc.) as long as it's applied consistently and doesn't collide with `nimbalyst`-prefixed identifiers already in the file (`fetchNimbalystSessions`, etc. — grep for `nimbalyst` before naming anything to avoid confusion between "NIMBLE" the PRD tool and "Nimbalyst" the unrelated existing herdr-adjacent integration already wired into this same file).
- JSON API response: return `[]` not `null` for an empty PRD list (existing repo convention per this project's CLAUDE.md and every other handler's empty-collection behavior).

### Edge Cases

- No `.nimble/` anywhere across any known project CWD → empty state, not an error (200 with empty/grouped-empty payload, not a 500).
- A `.nimble/prds/*` directory that has `tasks.yaml` but no `status.json` yet (e.g. a PRD still in `/nimble:plan`, like this PRD's own `status: "draft"` metadata state before task generation completed) → render with whatever is available, no alerts, no crash.
- Malformed/partial YAML or JSON in any of the three files → skip that one file's contribution (log, don't crash the whole panel), continue processing other PRDs/projects.
- Duplicate CWDs across many sessions → dedupe before scanning; do not re-`os.Stat` the same directory once per session.
- A CWD that no longer exists on disk (deleted/moved project) → skip silently, same as `fleet.go`'s existing `os.ErrNotExist` handling.

### Testing Requirements

- `internal/nimble` needs its own `_test.go` (see Deliverables table) — this is a brand-new package with zero existing test coverage to preserve, but `make test` runs with `-race`, so ensure any concurrent directory scanning (if you parallelize the CWD scan) is race-safe.
- No frontend test harness exists in this repo (confirmed in PRD §7: "this repo has no UI test harness"); UI correctness for the new tab is verified manually, not by an automated test task.
- Do not weaken or remove any existing test in `internal/timeline` or `internal/server` — this task should be additive only.

---

## Boundaries

### Files You MUST NOT Touch

From `.nimble/config.yaml` global `never_touch` list:
- `*.lock`, `extension/package-lock.json`, `.env`, `.env.*`, `*.db`, `*.db-journal`, `*.db-wal`, `*.db-shm`, `dist/`, `vendor/`, `internal/forecast/MODEL.pdf`, `internal/forecast/archive/**`, `bench-*.json`, `testdata-bench-*.json`

Plus, from this PRD's own §11 Agent Boundaries (applies to all tasks in this PRD):
- `internal/timeline/burn.go` and the `BurnSeries`/`fs.Burn` wiring in `internal/timeline/fleet.go` (out of scope, recently stabilized, unrelated to this task)
- `internal/store/sqlite.go` (this PRD has no SQLite involvement anywhere; the NIMBLE panel reads `.nimble/` files directly, never the usage-snapshot DB)
- `internal/parser/search.go` and `/api/sessions/search` (deliberately unused by this PRD — do not wire it into anything, including the NIMBLE panel)
- Any code inside `#tab-timeline` in `web/index.html`, or any `tl*`-prefixed JS function/variable (owned by 1a in this wave, and by 2b/3a/4a in later waves)

### Files Requiring Review

From `.nimble/config.yaml` `require_review`:
- `internal/auth/*`, `internal/store/sqlite.go`, `.goreleaser.yml`, `.github/workflows/*`, `extension/package.json`, `extension/src/*`

None of these are touched by this task's file list — no review-gated files are in scope for 1b.

---

## Dependencies

### Upstream Tasks

None — `depends_on: []` in `tasks.yaml`. Task 1b can start immediately.

### Downstream Impact

None — no task in this PRD lists `1b` in its `depends_on`. Task 2b depends on `1a` and `2a`, not `1b`; the NIMBLE panel is architecturally decoupled from the rest of this PRD by design (PRD §5: "it has no dependency on the gantt/event-list code at all").

**Before starting:** No dependency verification needed — this task has no upstream tasks to wait on. The only pre-existing state to verify is that `internal/parser/sessions.go`'s `SessionSummary.CWD` field and `DiscoverRecentSessions` function exist as documented above (confirmed live on `feature/timeline-feature-merger` as of this brief's generation) and that `web/index.html`'s tab structure matches the line numbers cited (also confirmed live).

---

## GitHub Context

**Issue:** TBD — not yet created (`status.json.tracker.issue_key` is `null`)
**Feature Issue (Parent):** TBD — same reason
**Branch:** `worktree/timeline-feature-merger-1b`
**Target:** `feature/timeline-feature-merger` (this PRD runs in `feature-branch` execution mode per `status.json.execution_mode`; the final merge to `main` on the `origin` fork happens once via `/nimble:merge` after all waves land, per PRD §7)

---

## Commit Guidelines

Use Conventional Commits:
```
feat(nimble): {description}

Co-Authored-By: Claude <noreply@anthropic.com>
```

Suggested scope: `nimble` for the new package/route, or split as `feat(nimble): add .nimble/prds discovery package` + `feat(web): add NIMBLE tab` if you prefer two commits over one. Types: feat, fix, refactor, test, docs, chore.

---

## Validation Checklist

Before creating PR:
- [ ] All success criteria met
- [ ] Build passes: `make build`
- [ ] `CGO_ENABLED=0 go build .` (or equivalent) still succeeds after adding `gopkg.in/yaml.v3` — this is the specific regression this PRD's risk table calls out for this task
- [ ] `go vet ./...` passes
- [ ] `make test` passes (race detector on)
- [ ] No `never_touch` files modified
- [ ] No edits inside `#tab-timeline` or to any `tl*`-prefixed function (1a's territory this wave)
- [ ] Tests added for `internal/nimble` (empty, missing-file, and populated-PRD cases)
- [ ] Branch rebased on `feature/timeline-feature-merger`

---

*Generated by NIMBLE Brief Writer*
*PRD: timeline-feature-merger | Task: 1b | Wave: 1*
