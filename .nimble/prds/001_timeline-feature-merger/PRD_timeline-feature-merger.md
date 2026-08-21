```yaml
# ============================================================
# PRD METADATA
# ============================================================
---
feature_name: "Timeline & Event Viewer Feature Merger"
feature_slug: "timeline-feature-merger"
owner: "jayden-eppcohen"
status: "draft"
created_date: "2026-08-20"
target_date: ""
phase: "Timeline v2"
scope_type: "integration"
github_project: ""
links: []
checkpoint_refs: []
cross_feature_blockers: []
---
```

---

## 1. Executive Summary

**One-liner:** Augment claumon's existing Timeline tab (fleet gantt + event list) with the
highest-value interaction patterns from the `agents-observe` plugin and a new panel surfacing
NIMBLE PRD execution state, without introducing a build step or replacing anything that works.

**What's changing:** Today, claumon's Timeline tab has a working but limited event viewer: no
filter/search UI, flat one-level subagent nesting, no way to pause a live-updating gantt to
explore it, and no keyboard-driven navigation. (An `/api/sessions/search` endpoint exists but
scans the entire ~/.claude transcript corpus by message text only — wrong shape for a per-
session event-list filter, so it stays unused; see R1.) After this ships: the event list is
filterable by tool/error/text via a client-side filter over the already-fetched event data,
multi-level subagent hierarchies render and expand correctly in both the gantt and
event list, a Live/Paused toggle lets users freeze the gantt to explore without fighting live
redraws, keyboard shortcuts jump between timeline regions, and a new panel shows active NIMBLE
PRDs (waves, health, blocked tasks) sourced from local `.nimble/prds/*` state.

**Who it's for:** Developers running claumon to monitor Claude Code sessions who currently
have to scroll manually through long event lists, can't see deep subagent chains, and get no
visibility into NIMBLE-orchestrated PRD execution without leaving the dashboard for the CLI.

**Why now:** Comparative research (2026-08-20) found `agents-observe` has already solved
filter/search, deep hierarchy, and pause/explore UX in a different (push/WebSocket/React)
architecture; the patterns are portable even though the code isn't. claumon's gantt was under
heavy daily iteration through 2026-08-19 and has now settled — this PRD is explicitly the
continuation of that work, not a competing fork of it.

**Done looks like:** Open the Timeline tab, type a tool name into the new filter box and see
the event list narrow live; expand a subagent that itself spawned a subagent and see it nest
correctly in both gantt and list; click "Pause" mid-stream, explore freely, click "Live" to
resume; press `f` to jump focus to the filter box; open a new "NIMBLE" panel and see any local
PRD's wave progress and blocked tasks.

**Primary risk:** `internal/timeline/timeline.go` and the gantt-rendering half of
`web/index.html` are dense, actively-shaped code (8 commits the day before this PRD was
written) — the recursive-hierarchy task in particular touches core parsing logic
(`BuildAgent`) and must not regress the fork-lineage/heat/liveness behavior landed in those
recent commits.

---

## 2. Problem & Context

**Problem statement:** claumon's Timeline tab under-serves two workflows agents-observe
supports well: (1) finding a specific event/error/tool-call in a long-running session without
scrolling, and (2) following a multi-level subagent chain (agent spawns agent spawns agent).
A third gap — no visibility into NIMBLE PRD execution inside claumon — isn't a timeline gap at
all but was raised as in-scope for this same merger effort.

**Supporting data / evidence:** See `research/findings.md` in this PRD folder — full read of
`agents-observe` source, claumon's `internal/timeline/*` + `web/index.html`, and the
`nimble:dashboard`/`karimo:dashboard` command docs, done 2026-08-20.

**What happens if we don't build this:** claumon stays weaker than agents-observe specifically
for debugging deep subagent trees and finding events in long sessions — the two workflows
where a text-only Sessions tab or a flat event list genuinely slow a user down. NIMBLE PRD
state stays CLI-only, requiring a context switch away from the dashboard.

**Strategic fit:** claumon's differentiator vs. agents-observe is architectural simplicity
(single Go binary, zero JS build step, pull-based). This PRD keeps that differentiator intact
by re-implementing agents-observe's *interaction patterns* natively rather than adopting its
stack.

---

## 3. Goals, Non-Goals & Success Metrics

### Goals

1. Make the event list filterable/searchable using the existing unused search backend.
2. Make subagent hierarchy genuinely recursive (N levels, not one flat level) in gantt and list.
3. Let a user pause the live gantt to explore without losing their place, then resume live.
4. Add a keyboard-driven navigation layer across the timeline regions.
5. Surface local NIMBLE PRD execution state (waves, health, alerts) as a new panel.

### Non-Goals

- No "constellation" force-directed session map — explicitly deferred by the user as
  out of scope for this PRD.
- No new build step, bundler, or frontend framework — `web/index.html` stays vanilla JS/CSS.
- No adoption of agents-observe's push/WebSocket/SQLite ingestion pipeline — claumon keeps
  its pull/file-watch/SSE model.
- No true session replay / action-replay engine (agents-observe doesn't have a real one
  either, despite README wording — see research findings).
- No audio/sound alerts (agents-observe doesn't have these; not requested).

### Success Metrics

| Metric | Baseline | Target | How Measured |
| ------ | -------- | ------ | ------------ |
| Event list filter | No UI, backend unused | Filter by tool/error/text, live-narrows list | Manual test in browser |
| Hierarchy depth rendered | 1 level (flat) | N levels, correctly nested | Manual test against a session with grandchild subagents |
| Gantt explore-without-losing-live | Not possible | Pause/Live toggle, resumes cleanly | Manual test |
| Keyboard nav | None | Region-jump shortcuts functional | Manual test |
| NIMBLE visibility | CLI-only (`/nimble:dashboard`) | Panel in claumon shows same wave/health data | Manual test against this PRD's own `.nimble/` state |

---

## 4. Requirements

### Must Have (blocks launch)

| ID | Requirement | Acceptance Criteria |
| -- | ----------- | -------------------- |
| R1 | Client-side filter/search over the event list | Typing in the event-list filter box narrows visible rows live, matching client-side against already-fetched event fields (Title/Detail/Full/Output/IsError); filter-by-tool-name and filter-by-error-state available as chips alongside free text |
| R2 | Recursive subagent hierarchy | `BuildAgent` nests subagents of subagents; gantt shows indented child rows per depth; event list allows expanding a subagent row into its own subagent's event list, arbitrarily deep |

### Should Have (important, not blocking)

| ID | Requirement | Acceptance Criteria |
| -- | ----------- | -------------------- |
| R3 | Live/Paused toggle for the gantt | Clicking "Pause" freezes SSE-driven redraws of the gantt (selection/scroll state preserved); "Live" resumes normal refresh behavior |
| R4 | NIMBLE PRD panel | New tab/panel lists local `.nimble/prds/*` folders with status, wave progress, and any BLOCKED/STALE alerts, read directly from `tasks.yaml`/`execution_plan.yaml`/`status.json` |

### Could Have (nice to have, cut first)

| ID | Requirement | Acceptance Criteria |
| -- | ----------- | -------------------- |
| R5 | Keyboard nav layer | At minimum: a shortcut to focus the filter box and arrow-key navigation within the event list; stretch: region-jump across gantt/list/filters/NIMBLE panel |

---

## 5. UX & Interaction Notes

**User Experience:**
- Filter box sits above the event list (not the gantt) — text input plus two toggle chips
  (tool name, error-only). Matches agents-observe's two-tier pattern but stays a plain
  `<input>` + `<button>` pair, no command-palette dependency.
- Recursive subagent rows keep the existing caret-expand interaction (`web/index.html`
  gantt) and existing lazy-fetch-per-expand pattern (event list) — just no longer capped
  at one level.
- Live/Paused toggle is a single button near the existing time-window selector (1h/3h/1d/
  3d/1w). Pausing does not stop data collection server-side — it only stops the frontend
  from redrawing on SSE `"sessions"` events until resumed.
- NIMBLE panel is a new top-level tab alongside claumon's existing dashboard/memories/graph/
  timeline tabs (`web/index.html:2244`), not nested inside Timeline — it has no dependency on
  the gantt/event-list code at all, and its `index.html` edits are confined to the tabs array,
  the activation switch, and a new self-contained panel block, to avoid colliding with 1a's
  filter-box edits in the same file.

**Key screens & states:**
- Empty state (NIMBLE panel): "No NIMBLE PRDs found in this repo" when `.nimble/prds/` is
  absent or empty.
- Empty state (filter): filtering to zero results shows the same "no events" state the
  event list already uses for an empty session.
- Loading/error states: reuse existing Timeline tab patterns (no new spinner/skeleton design
  needed).

**Accessibility:** Keyboard nav (R5) is itself the accessibility improvement for this PRD;
no separate a11y requirement beyond keeping focus order sane when the filter box and new
panel are added.

**Responsive:** Out of scope — claumon's dashboard is desktop-oriented today and this PRD
doesn't change that.

---

## 6. Dependencies & Risks

### Cross-Feature Blockers

_None — first PRD in this repo's `.nimble/` history._

### External Blockers

_None._

### Internal Dependencies

- `timeline.Event` fields already delivered by `GET /api/timeline/{id}` (`internal/timeline/
  timeline.go`) — R1's entire data source; client-side filter, no backend change.
- `internal/timeline/timeline.go` (`BuildAgent`, `AgentRef.SpawnDepth`) and `fleet.go`
  (`FleetAgent`, `agentSpans`) — R2's core change (task 2a), consumed by 2b's rendering.
- `internal/server/sse.go` — R3 only needs frontend-side gating of the existing `"sessions"`
  SSE handler, no server change required.
- `.nimble/prds/*/{tasks.yaml,execution_plan.yaml,status.json}` plus `parser.SessionSummary.
  CWD` (for project discovery) — R4's entire data source; new `internal/nimble` package,
  `gopkg.in/yaml.v3` is the one new dependency this PRD introduces.

### Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
| ---- | ---------- | ------ | ---------- |
| Recursive hierarchy change regresses recent gantt work (fork curves, heat, liveness, row ordering — all landed 2026-08-19) | Medium | High | 2a (Go) explicitly must not touch `BurnSeries`/`fs.Burn` wiring; 2b (frontend) scoped to touch only agent-nesting render logic; must re-run manual checks against fork/heat/liveness behavior before merge; automated gate is `make test`/`go vet`, with new timeline package tests covering a 3-level nested-agent fixture |
| Filter UI (1a), NIMBLE panel (1b), and keyboard-nav (4a) all touch `web/index.html` | Medium | Low | 1a and 1b confined to disjoint regions (event-list filter box vs. tabs array + new panel block); 2b/3a/4a waved after 1a so each lands on a settled file |
| NIMBLE panel scope creep toward re-implementing the full CLI dashboard | Low | Medium | R4 acceptance criteria capped to status/wave/alerts display only — no health-score formula, no velocity/resource sections in this PRD |
| New dependency (`gopkg.in/yaml.v3`) breaks `CGO_ENABLED=0` cross-compilation | Low | Medium | yaml.v3 is pure Go; verify `make build` (cross-target via goreleaser config) still succeeds after adding it |

---

## 7. Rollout Plan

**Phase/level:** Single feature, one repo, no staged rollout — claumon has no user base
beyond the maintainer to stage against.

**Deployment strategy:** Feature branch (`feature/timeline-feature-merger`), one PR per
task merging into the feature branch, then one final PR to `main` on the `origin` fork
(`Olly-Oxen-Free/claumon`) — not the `fabioconcina/claumon` upstream — via `/nimble:merge`.

**Rollback plan:** Each task is a separate PR; revert the offending PR rather than the whole
feature branch if one task regresses something.

**Monitoring:** None automated — manual verification in-browser per task (this repo has no
UI test harness; `make test`/`make vet` cover the Go side only).

---

## 8. Milestones & Release Criteria

| Milestone | What's True When Done | Target Date |
| --------- | --------------------- | ----------- |
| Wave 1 merged | Filter/search UI live; NIMBLE panel live; hierarchy Go parsing (2a) landed; all three independently mergeable | — |
| Wave 2 merged | Recursive hierarchy renders correctly in gantt + event list (2b), no regressions in fork/heat/liveness | — |
| Wave 3 merged | Live/Paused toggle functional | — |
| Wave 4 merged | Keyboard nav layer functional | — |

**Release criteria (what must be true to ship):**

- `make test` and `make vet` pass on the feature branch.
- Manual verification of each Success Metric in §3 against a real session with nested
  subagents.
- No regression in fork-curve, heat-view, or liveness rendering (2026-08-19 work).

---

## 9. Open Questions

_None outstanding — all scoping questions resolved during the interview (ship shape, feature
priority, review gate, no-build-step constraint, augment-not-replace constraint)._

---

## 10. Checkpoint Learnings

First PRD in this repo's `.nimble/` history — no prior checkpoints to draw on.

---

## 11. Agent Boundaries (Phase-Specific)

**Files the agent should reference for patterns:**

- `web/index.html` — existing `flRender`/`tlRenderEvents`/`tlEventRow`/`EV_ICONS` for
  established visual conventions (icon registry explicitly shared with agents-observe's
  own registry per existing code comment).
- `internal/timeline/timeline.go`, `internal/timeline/fleet.go` — existing parsing/build
  patterns to extend, not replace.
- `internal/parser/sessions.go` (`SessionSummary.CWD`) — existing pattern for project
  discovery, reused by 1b instead of inventing a new mechanism.
- `internal/parser`, `internal/pricing` — examples of the one-package-per-subsystem
  convention `internal/nimble` (1b) should follow.

**Files the agent should NOT touch (beyond the global `never_touch` list):**

- `internal/timeline/burn.go` / heat-view color logic and `fs.Burn` wiring in `fleet.go` —
  out of scope, recently stabilized (`af8dac2`, `f7cd4b6`), do not touch unless a task
  explicitly requires it.
- `internal/store/sqlite.go` — this PRD has no SQLite involvement; NIMBLE panel reads
  `.nimble/` files directly, not the usage-snapshot DB.
- `internal/parser/search.go`, `/api/sessions/search` — deliberately not used by this PRD
  (wrong shape for a per-session filter, see R1); do not wire it in.

**Architecture decisions already made (don't re-decide):**

- No build step / no frontend framework — vanilla JS/CSS in `web/index.html` throughout.
- Augment existing Timeline tab; do not add a parallel/competing timeline view.
- Event-list filter (R1) is client-side over already-fetched data, not backed by
  `/api/sessions/search`.
- NIMBLE panel is a new top-level tab, decoupled from the Timeline tab's code; it may add
  exactly one new dependency, `gopkg.in/yaml.v3`, for parsing `.nimble/prds/*` YAML.
- Filter-box focus shortcut is `f`, not `/` (`/` is already bound to Memories-tab search,
  `web/index.html:2256-2267` — do not rebind or overload it).
- Gate model: automated (`make test` + `go vet`) per task, no human-review gate — this PRD
  is itself the "gantt work in progress," not a conflicting concurrent effort.

**Known gotchas discovered since research:**

- `BuildAgent` never calls `attachAgents` at all (only `Build` does), and `attachAgents`
  only matches `toolUseId` against the root session's own events — so depth-2+ subagents
  are actively misattributed to an unrelated root-level `Task` row today, not merely
  un-nested. This is live data corruption on already-shipped sessions, discovered by
  brief-writer during Phase 1 — fix for R2 (task 2a) is a session-wide `toolUseId → agent`
  pool matched depth-first/bottom-up, not a rename/refactor. `AgentRef.SpawnDepth` is already parsed
  and may cover part of the depth-tracking need.
- `FleetAgent` (`fleet.go:158-159`) is currently a flat struct with no children field —
  2a must add one before 2b can render nesting.

---

## Research Findings

**Last Updated:** 2026-08-20
**Research Status:** Approved
**Research Rounds:** 1

Full findings: `research/findings.md` in this PRD folder (three-system comparison: claumon
current state, agents-observe plugin, nimble/karimo `/nimble:dashboard`).

---

## Agent Tasks

> Tasks are stored in `./tasks.yaml`. Execution plan in `./execution_plan.yaml`.

**Task Summary:**

| ID | Title | Complexity | Priority | Dependencies |
|----|-------|------------|----------|--------------|
| 1a | Client-side filter/search over the event list | 3 | must | - |
| 1b | NIMBLE PRD panel (new tab reading local `.nimble/prds/*` state) | 5 | should | - |
| 2a | Recursive subagent hierarchy — Go parsing | 4 | must | - |
| 2b | Recursive subagent hierarchy — gantt and event-list rendering | 4 | must | 1a, 2a |
| 3a | Live/Paused toggle for the gantt | 4 | should | 2b |
| 4a | Keyboard nav layer across timeline regions | 4 | could | 1a, 3a |

---

*Generated via `/nimble:plan` interview, 2026-08-20*
