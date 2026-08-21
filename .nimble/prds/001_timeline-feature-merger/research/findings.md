# Research: timeline-feature-merger

Comparative research across three systems ahead of merging desired timeline/event-viewer
features into claumon. Gathered via three parallel Explore agents reading live source
(not docs/READMEs) on 2026-08-20.

## System A — claumon (current state)

Single Timeline tab, two stacked panels, not two separate features:

- **Fleet gantt** (`web/index.html` `flRender`, `internal/timeline/fleet.go`) — one row per
  Claude session, subagents nested one flat level beneath. X-axis = wall-clock time,
  selectable window (1h/3h/1d/3d/1w), fixed px/hour, scrolls rather than compresses.
  Bars: solid = active span, dashed = idle (context past cache TTL), true gaps undrawn.
  Forks (`/branch`, `/fork`) draw as a child bar joined via SVG curve at the divergence point.
- **Event list** (`tlRenderEvents`/`tlEventRow`) — full transcript detail: prompts, messages,
  tool calls (args + result, capped 4000 chars), thinking blocks, compaction boundaries,
  subagent spawns (expandable into that agent's own event list, lazily fetched). Selecting
  a gantt bar filters + drives the list below it; scrolling the list draws a read-position
  marker back on the gantt. One coupled feature, not two independent views.
- **Heat** (`flHeat`, opt-in via `?burn=1`): recolors bars by tokens-per-time ("burn rate"),
  log-scale gradient, costs a full transcript read per session so it's off by default.
- **Live updates**: generic SSE broker (`internal/server/sse.go`), file-watcher-driven
  (`internal/watcher`), 750ms/3s debounce. No dedicated timeline SSE event type — a
  `"sessions"` broadcast triggers a scroll-aware refetch. 15s/30s polling exists only as
  SSE-drop fallback.
- **Storage**: no SQLite for timeline data — everything derived live from
  `~/.claude/projects/**/*.jsonl` on each request, with in-memory caches keyed by
  file path+size. SQLite (`internal/store/sqlite.go`) only holds usage snapshots/daily
  aggregates for the separate usage-gauge feature.
- **Gaps identified**:
  - Subagent hierarchy is flat, not recursive — a subagent's own subagent shows as an
    inert `Task` row, not an expandable nested agent (`internal/timeline/timeline.go`,
    `BuildAgent` never calls `attachAgents`).
  - `/api/sessions/search` (full-text search across transcripts) exists server-side
    (`internal/server/handlers.go`, `internal/parser/search.go`) with **zero frontend
    wiring** — ready-made backend, no UI.
  - No filter chips beyond single-session focus; can't filter by tool/error/text.
  - No session replay, no cross-session correlation beyond lineage + time overlap.
  - No standalone burn/cost sparkline independent of the bar-gradient.
- **Active iteration risk**: last 8 commits (all 2026-08-19) are incremental fleet-gantt
  polish (heat-view color fix, fetch-on-toggle fix, row ordering, opacity-system removal,
  Nimbalyst session joins, fork-lineage unification, PID-based liveness). The gantt surface
  is a moving target under active development; any merge plan touching it should coordinate
  rather than fork. The event-list side is comparatively static — lower-risk landing zone.

## System B — agents-observe plugin

React 19 SPA + Node/Hono server, WebSocket live updates, SQLite storage, Docker-first.
Deepest feature set of the three, most relevant as a feature donor:

- **Live "Activity" timeline** (oscilloscope pattern): one lane per agent, dots continuously
  animate right-to-left via the Web Animation API (not re-render-per-tick — explicit
  CPU-profiling discipline in comments). Click a dot → scrolls event stream to that event;
  scrolling the stream also moves the timeline (bidirectional sync, `lib/scroll-sync.ts`).
- **Live/Rewind toggle**: "Rewind" freezes the oscilloscope and swaps to a fully explorable,
  proportional pixel-per-ms historical timeline with drag-to-scroll and position restore.
- **Event stream**: virtualized list, auto-follow (tail -f) toggle, full keyboard scroll,
  flash-highlight on jump-to-event.
- **Constellation view**: alternative dashboard home — force-directed physics-simulated star
  map, one glowing star per session (size/heat = activity/recency), idle sessions cool and
  drift, click a star → radial drill-in agent-hierarchy tree in the same zoomed SVG space.
  Biggest visual differentiator of the three systems; also the biggest build.
- **Recursive multi-agent hierarchy**: `parentAgentId` derived server-side, proper nesting
  (not flat), depth-first stable color assignment reused across timeline/tree/event-row.
- **Filters**: two-tier pill filters + free-text search + precomputed per-event `searchText`,
  plus user-defined saved regex filters compiled via RE2 (linear-time, ReDoS-safe).
- **Keyboard nav layer**: region-jump shortcuts (`/`/`s` search, `a` agents, `f` filters,
  `b` sidebar, `e` event stream) plus arrow-key nav within each region.
- **Notifications**: visual only — pulsing bell + favicon swap on pending `Notification`
  events. Confirmed via grep: **no audio/sound alerts anywhere in the codebase.**
- **Cost breakdown UI**: per-session/per-prompt table, cache-tier (5m/1h) accounting,
  sortable — computed by parsing raw transcript JSONL directly (separate from the
  hook-event pipeline), same source-of-truth pattern claumon already uses.
- No true replay engine despite README wording — "full replay" maps entirely to the
  Rewind scrub feature, not an action-replay system.
- Data pipeline is push-based (Claude Code hooks → HTTP POST → SQLite → WS broadcast),
  architecturally different from claumon's pull/file-watch model — hierarchy/filter/rewind
  *patterns* are portable, the ingestion pipeline is not.

## System C — nimble/karimo `/nimble:dashboard`

Point-in-time CLI text report, **not** a web UI — explicitly states it "replaces the
planned web dashboard." No live/streaming mechanism; each invocation re-derives state
(120s cache, `--refresh` bypasses) from `status.json` + `tasks.yaml` + `execution_plan.yaml`
plus live `git`/`gh` queries.

- Sections: Executive Summary (health score, formula: `taskSuccessRate*0.30 +
  reviewEfficiency*0.25 + stalledTaskPenalty*0.20 + parallelUtilization*0.15 +
  blockedTaskPenalty*0.10`), Critical Alerts (BLOCKED/STALE/CRASHED/CONFLICTS/ORPHANED,
  each with an action line), Execution Velocity (7-day completion/loop-efficiency/ETA),
  Resource Usage (Sonnet/Opus split, escalation rate, loop-count histogram), Recent Activity.
- Waves are dependency-ordered batches from a greedy topological sort, stored once in
  `execution_plan.yaml` (immutable), runtime wave status tracked in `status.json.waves`.
- Task status enum: `queued → running → in-review → done`, plus `needs-revision`,
  `needs-human-review`, `failed`, `crashed`, `blocked`, `paused`, `awaiting-human`.
- karimo is a byte-identical rename-fork of nimble (dashboard.md diffs only in
  frontmatter + attribution footer) — no functional divergence to reconcile.
- To surface this in claumon: read `.nimble/prds/*/{tasks.yaml,execution_plan.yaml,
  status.json}` directly (all local files, no API needed) and render wave/health/alert
  state as a panel — this is a data-format integration, not a live-protocol integration.

## Cross-system takeaways for scoping

- claumon's event-list side (not the actively-iterated gantt) and its unused search
  backend are the lowest-risk places to land new work.
- agents-observe's rewind/filter/hierarchy patterns are UI/interaction patterns to
  re-implement against claumon's own pull-based data model, not code to port directly —
  the ingestion pipelines are architecturally incompatible (push+WS+SQLite vs.
  pull+file-watch+SSE+live-JSONL-parse).
- NIMBLE dashboard integration is a file-format read, independent of the timeline/event
  surface — can be scoped as a separate panel/tab with no coupling to the gantt work.
- Constellation view explicitly deferred by user as out of scope for this PRD.
