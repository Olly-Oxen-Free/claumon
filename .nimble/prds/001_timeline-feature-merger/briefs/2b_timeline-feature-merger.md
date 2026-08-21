# Task Brief: 2b

**Title:** Recursive subagent hierarchy — gantt and event-list rendering
**PRD:** timeline-feature-merger
**Priority:** must
**Complexity:** 4/10
**Model:** sonnet
**Wave:** 2
**Feature Issue:** #TBD (no tracker issue created yet — `status.json.tracker.issue_key` is `null` at brief-writing time; confirm with the PM agent before opening a PR)

---

## Objective

Today claumon's fleet gantt and event list each render only one flat level of subagent
nesting — a subagent's own subagent shows as an inert row, not a nested one. Task 2a adds
recursive parsing on the Go side (nested `FleetAgent`/agent data to arbitrary depth); this task
makes the frontend actually draw that nesting — indented rows in the gantt, recursively
expandable rows in the event list — without touching the fork-curve, heat-view, or
PID-liveness rendering that landed the day before this PRD was written.

---

## Context

**Parent Feature:** Timeline & Event Viewer Feature Merger (`timeline-feature-merger`) — see
`.nimble/prds/001_timeline-feature-merger/PRD_timeline-feature-merger.md`

claumon's Timeline tab has two coupled panels: a fleet gantt (`flRender`, one row per session,
subagents nested one flat level beneath) and an event list (`tlRenderEvents`/`tlEventRow`,
full transcript detail with subagent rows that lazily expand into that agent's own event
list — currently capped at one level). `agents-observe`, the plugin this PRD is comparing
against, already does proper N-level nesting with depth-first stable rendering; this task
brings claumon's own gantt/list to parity, reusing claumon's existing caret-expand and
lazy-fetch interaction patterns rather than adopting agents-observe's stack.

This task is part of **Wave 2** — the only task in that wave, gated behind both 1a (event-list
filter, Wave 1) and 2a (Go-side recursive parsing, Wave 1). Both must be merged into
`feature/timeline-feature-merger` before this task starts; 3a (Live/Paused toggle, Wave 3) then
depends on this task landing cleanly.

The gantt is explicitly called out in the PRD as the primary risk: 8 commits landed the day
before this PRD was written (heat-view colour fix, fetch-on-toggle fix, row ordering,
opacity-system removal, fork-lineage unification, PID-based liveness) — all of that work sits
in the exact function (`flRender`) this task edits, and must not regress.

---

## Research Context

From `.nimble/prds/001_timeline-feature-merger/research/findings.md` (System A — claumon
current state):

### Patterns to Follow

- **Session lineage indent:** `flRender` already computes a depth-based indent for
  parent/child *sessions* (forked via `/branch`/`/fork`) — `const indent = (s._depth || 0) * 12;`
  applied as `style="padding-left:${8 + indent}px"` on `.fl-label`
  (`web/index.html:4534`, applied at `:4696`). This is the pattern to mirror for *agent* nesting
  depth — same `depth * 12px` convention, same inline-style-over-CSS-default approach — but it
  is a **different depth axis** (session lineage vs. agent spawn depth). Do not reuse `s._depth`
  itself or conflate the two; add a separate depth counter for the agent recursion.
- **Expand/collapse state as a Set:** `let flExpanded = new Set();` (`web/index.html:4120`,
  comment: "sessions whose agents are shown") is the existing pattern for gantt-level expand
  state, toggled in the `.fl-label` click handler (`web/index.html:4954-4969`) and checked in
  `flRender` (`web/index.html:4518`: `const open = flExpanded.has(s.session_id) || s.session_id === flFocused;`).
  Follow this same pattern for agent-level expand state (new Set, keyed by `agent_id` — agent
  IDs are globally unique `agent-<hex>` strings per `safeAgentID` in
  `internal/timeline/timeline.go:250-260`, so one flat Set works at any depth without collision).
- **Lazy per-level fetch, event list:** the existing subagent-expand click handler
  (`web/index.html:3970-4020`) already fetches `/api/timeline/{sessionId}/agents/{agentId}` and
  drops the returned events into a `#tl-agent-{agentId}` box via
  `tlEventRow(e, sessionId)` — see "Known Issues to Address" below for why this may need **no
  structural change** for the event-list side.
- **Icon registry:** `EV_ICONS` (`web/index.html:3731-3745`) and `evIcon()`
  (`:3747-3761`) are the shared icon lookup with agents-observe's own registry — do not add a
  parallel icon mechanism for nested agent rows; reuse `evIcon`/the existing `ic-bot` treatment.

### Known Issues to Address

- ⚠️ **The gantt has no per-agent caret today.** The current agent-row loop
  (`web/index.html:4707-4722`) unconditionally renders one `.fl-row.agent` per entry in
  `s.agents` with no expand affordance and no recursion — it was never built to go deeper
  because there was nothing deeper to show. This is the block that must become recursive.
- ⚠️ **The `.fl-label` click handler currently special-cases agent rows to always "open."**
  `web/index.html:4954-4969`:
  ```js
  el.addEventListener('click', () => {
    if (row.classList.contains('agent')) {
      flOpenAgent(row.dataset.session, row.dataset.agent);
      return;
    }
    const id = row.dataset.session;
    if (flFocused === id) return;
    if (flExpanded.has(id)) flExpanded.delete(id); else flExpanded.add(id);
    flRender();
  });
  ```
  Session rows: **label click toggles expand**, **bar click opens/selects** (see the
  `.fl-bar` handler at `:4971-4979`, which calls `flOpenAgent` when `el.dataset.agent` is set,
  and otherwise focuses/selects the session). Agent rows currently collapse both of those into
  one action because an agent never had children to expand. Once agents can have children, this
  needs a third branch: an agent row **with children** should toggle its own expand-Set on
  label click (mirroring the session pattern) rather than always calling `flOpenAgent`; an agent
  row **without children** keeps today's behaviour unchanged (this is what keeps
  "visually/behaviourally unchanged for sessions without deep nesting" true). The bar click
  (`:4971-4979`) can keep calling `flOpenAgent` unconditionally at every depth — that part
  doesn't need to change.
- ⚠️ **`flOpenAgent` will not reliably deep-link past one level.** `flOpenAgent`
  (`web/index.html:5008-5017`) calls `flSelectSession(sessionId)`, which calls `tlLoad`
  (`:3786`), which does `tlExpanded.clear()` (`:3788`) on every load — wiping any previously
  expanded subagent rows in the event list. `flOpenAgent`'s polling loop then looks for
  `.tl-row.subagent[data-agent="agentId"]` in the freshly-rendered (collapsed) list. For a
  *top-level* agent this still works, because the top-level subagent rows are drawn immediately.
  For a *grandchild* agent, its row only exists once its parent subagent row has itself been
  expanded and fetched — which `flOpenAgent` never does. **This is a real gap, not a regression**
  (there was no way to deep-link to a nested agent before this task either, because nested
  agents didn't render at all). Success criteria do not require gantt-to-list deep-linking below
  depth 1 to work — treat it as a known, documented limitation unless it's trivial to fix by
  walking the ancestor chain and expanding each `.tl-children` box before polling for the
  target row. Do not spend disproportionate effort here; call it out in the PR description if
  left unresolved.
- ⚠️ **Event-list recursion may already work structurally — verify, don't assume, and don't
  rewrite from scratch.** Tracing the call chain: `tlRenderEvents` (`:3876-3884`) calls
  `tlEventRow(e, sessionId)` (`:3829-3870`) for every event, where `sessionId` is always the
  *top-level* session ID passed down from `tlLoad`. When a subagent row is expanded
  (`:3970-4020`), the fetched sub-events are rendered via
  `(sub.events || []).map(e => tlEventRow(e, sessionId))` (`:4016`) — reusing the **same**
  closure-captured `sessionId`, not the nested agent's own ID. Because `BuildAgent(claudeDir,
  sessionID, agentID)` (`internal/timeline/timeline.go:229-247`) resolves `agentID` against a
  **flat** `<session>/subagents/` directory regardless of nesting depth (confirmed by reading
  `attachAgents` at `internal/timeline/timeline.go:443-448` and `agentSpans` at
  `internal/timeline/fleet.go:520-525` — both list one flat `subagents/` dir, not a
  per-agent nested directory tree), the existing `/api/timeline/{sessionId}/agents/{agentId}`
  endpoint should already resolve a grandchild agent correctly once 2a's Go-side
  `attachAgents` recurses to populate deeper `kind: 'subagent'` events. Combined with the CSS
  nesting already in place (`.tl-children { margin-left:24px; border-left:2px dashed
  var(--purple); padding-left:8px; }`, `web/index.html:825` — each expanded box nests visually
  inside its parent via ordinary block-level margin, independent of row depth), and the fact
  that the click delegation on `#tl-list` (`:3970`) uses `.closest()` so it resolves correctly
  regardless of how deeply a `.tl-row.subagent` is nested in the DOM — **the event-list half of
  this task may require little to no JS change**, only verification against a real 3-level
  fixture once 2a lands. Do not rewrite `tlEventRow`/the click handler speculatively; confirm
  first, and only change what a real nested session proves is broken (e.g. if depth indent
  looks wrong — see next point — or if `tlExpanded`/`box.dataset.loaded` collide across levels,
  which they should not, since both are keyed by globally-unique `agent_id`).
- ⚠️ **Event-row depth class is coarse.** `tlEventRow`'s indent logic
  (`web/index.html:3850-3854`) only distinguishes depth `0` (prompt/message/compact) from depth
  `1` (everything else, including tool calls *and* subagent rows) — a grandchild's own tool
  calls get the same `d1` class as a top-level tool call. Visual nesting comes entirely from the
  `.tl-children` box's own `margin-left`, not from this class. This is very likely fine as-is
  (each level of nesting still reads visually deeper via the cascading `margin-left`), but check
  it against a real 3-level fixture — if the flat `d1` class reads ambiguous next to 24px
  margins stacking three deep, that's a legitimate, narrowly-scoped fix (do not restructure the
  depth system, just confirm the existing box-model nesting reads clearly).

### Recommended Approach

- In `flRender`, extract the current one-level agent-row block (`web/index.html:4707-4722`)
  into a small recursive helper (e.g. `flAgentRows(s, agents, depth, sel)` returning an array of
  row-HTML strings) that closes over `clampBar`, `canvas`, `ticks`, `nowMark` from the enclosing
  `flRender` scope. Call it once per session with `depth = 0`; have it push one `.fl-row.agent`
  per entry, then — if the agent has children and is itself expanded — recurse with
  `depth + 1`, appending the child rows immediately after the parent's own row (same
  flat-array-of-row-strings approach `flRender` already uses for the session/agent split at
  `:4707-4722`, no new layout system needed).
- Indent: `style="padding-left:${22 + depth * 12}px"` on `.fl-label.agent`, keeping the
  existing `22px` base from the CSS default (`web/index.html:628`,
  `.fl-label.agent { padding-left:22px; }`) as depth-0's value, then adding `depth * 12` for
  each further level — consistent with the session-lineage indent's own `* 12` step
  (`:4534`).
- Caret: reuse the same `▾ `/`▸ ` glyph convention the session label already uses
  (`web/index.html:4521`: `const caret = agents.length ? (open ? '▾ ' : '▸ ') : '';`), prefixed
  onto the agent's own name, shown only when the agent has children.
- Confirm the field name 2a actually lands with (`FleetAgent.Children` per the PRD's Agent
  Boundaries section, but read `internal/timeline/fleet.go` directly once 2a is merged — do not
  assume the JSON key without checking; it will be something like `children` per the struct's
  existing `json:"...,omitempty"` convention seen on every other `FleetAgent` field,
  `internal/timeline/fleet.go:158-166`).

### Dependencies

**File Dependencies:**
- `internal/timeline/fleet.go` — `FleetAgent` (currently `internal/timeline/fleet.go:158-166`,
  no children field yet) gains a children field from task 2a; 2b's gantt code reads it.
- `internal/timeline/timeline.go` — `attachAgents` (`:443-...`) becomes recursive under 2a;
  2b's event-list code depends on this recursion actually populating nested `kind: 'subagent'`
  events, not on any new endpoint (the existing `/api/timeline/{id}/agents/{agent}` route,
  registered at `internal/server/server.go:42`, needs no change — see Known Issues above).
- `web/index.html` — task 1a adds a filter box to `.tl-listbar`
  (`web/index.html:1552-1559`, currently just the "Events" title, `tl-filterchip`, sort button,
  and paused indicator) and very likely wires filtering into or around `tlRenderEvents`
  (`:3876-3884`) — the same function 2b's event-list verification touches. Re-read the live
  `tlRenderEvents`/`tlEventRow` functions after 1a is merged (line numbers above will have
  shifted); confirm 1a's filter is applied at the `events` array level (before the
  `.map(tlEventRow)` call) so it composes with recursion rather than needing to be re-derived
  per nesting level. 2b must not touch the filter box HTML/chip JS itself — that stays 1a's.

**Library Dependencies:** None — vanilla JS/CSS only, per the PRD's standing architecture
decision (no build step, no framework).

---

## Requirements

1. **Gantt nesting (`flRender`):** subagent rows in the fleet gantt render at their true depth,
   indented per level, with an expand/collapse caret on any agent that itself has children.
   Collapsing a parent agent hides its descendants; expanding it shows only its direct children
   until they too are expanded. A session or agent with no nested children renders exactly as it
   does today (no caret, no extra indent, no behavioural change).
2. **Event-list nesting (event delegation on `#tl-list`):** expanding a subagent row that itself
   spawned a subagent shows a further expandable subagent row inside it, lazily fetched only
   when that inner row is expanded — arbitrarily deep, not capped at one level.
3. **No regressions:** fork-curve (`flForkCurve`), heat-view (`flHeat`/`flHeatColor`/
   `flHeatGradient`/`flHeatAt`), and PID-based liveness (`s.is_running`, the `live` row class,
   the green line colour) render identically to today for any session that has no nested
   (depth ≥ 2) agents — these three systems live in code paths this task does not touch
   (`web/index.html:4163-4206` for the fork curve; `:4286-4337` and the `flHeat` branches
   inside `flSeg`/the main bar block, `:4626-4692`, for heat; `s.is_running`-driven classes
   throughout the session-row block, `:4694-4705`, for liveness) — verify by inspection that
   none of those line ranges are edited, not just by eyeballing the rendered result.

---

## Success Criteria

Complete ALL criteria before marking task done:

- [ ] A session with a subagent that itself spawned a subagent renders 2+ levels deep in the
      gantt, correctly indented (each level visibly further right than its parent, using the
      `depth * 12px` step described above or an equivalent consistent step)
- [ ] Each agent row with children shows an expand/collapse caret; toggling it shows/hides only
      that agent's direct children (not the whole subtree at once) and does not change any
      unrelated row's expand state
- [ ] An agent row with no children renders with no caret and no extra indent beyond today's
      fixed `22px`, identical to current behaviour
- [ ] The same multi-level nesting expands correctly in the event list: expanding a subagent row
      whose own transcript contains a further subagent spawn shows an expandable child row,
      lazily fetched only on that child's own expand click (verified against a real or
      constructed 3-level session, not just code inspection)
- [ ] Fork-curve rendering (`flForkCurve`) is pixel-for-pixel unchanged for any forked session
      with no nested (depth ≥ 2) agents — confirm by diffing rendered output or by inspection
      that `web/index.html:4163-4206` and its call site at `:4689` are untouched
- [ ] Heat-view rendering (`?burn=1` / the `heat` button) is visually unchanged for sessions
      without deep nesting — confirm `flHeat`, `flHeatColor`, `flHeatGradient`, `flHeatAt`
      (`web/index.html:4286-4337`) are untouched, and that no heat styling is added to nested
      agent bars (agent bars carry no heat gradient today — keep it that way, do not extend heat
      to agent rows as part of this task)
- [ ] PID-based liveness rendering (`live` class, green line colour driven by `s.is_running`) is
      unchanged for session rows; nested agent rows carry no liveness indicator, matching
      today's single-level agent rows (which also carry none)
- [ ] `make test` and `go vet` pass (no Go files are touched by this task, but both must still
      pass cleanly on the branch)
- [ ] Manual verification performed in-browser against a real session with 2+ levels of
      subagent nesting (this repo has no UI test harness — see PRD §7 Monitoring — so this is
      the actual gate, not a formality)

**All criteria must pass before task is complete.**

---

## Files to Modify

| File | Action | Purpose |
|------|--------|---------|
| `web/index.html` | modify | Recursive agent-row rendering in `flRender` (gantt); verification (and only-if-needed fixes) of recursive subagent expansion in `tlRenderEvents`/`tlEventRow`/the `#tl-list` click delegation (event list) |

### File Ownership Notes

- This is the fourth task in this PRD to touch `web/index.html` (after 1a, 1b, and — on the Go
  side only — 2a touches Go files, not this one). By the time this task starts, 1a and 2a are
  already merged into `feature/timeline-feature-merger` (both are direct dependencies); re-read
  the live file rather than trusting any line number in this brief once 1a's filter box has
  landed, since it edits `.tl-listbar` (`web/index.html:1552-1559` today) and likely
  `tlRenderEvents` itself.
  - 1b (NIMBLE panel, Wave 1, independent) is scoped to the `tabs` array/activation switch
    (`web/index.html:2244`, `:2251-2252`) and a new self-contained panel block — no overlap with
    this task's gantt/event-list region.
  - 3a (Live/Paused toggle, Wave 3, depends on this task) will gate the SSE `"sessions"`
    handler's calls to `tlRefresh()`/`flRefreshSoon()` behind a paused flag — it depends on this
    task's rendering functions (`flRender`, `tlRenderEvents`) keeping their existing call
    signatures and being safe to invoke repeatedly; do not change what triggers a re-render, only
    how many levels it draws.
- Stay inside the gantt's agent-row block and the event list's subagent-expansion path. Do not
  touch the session-level bar/fork-curve/heat code (`web/index.html:4671-4705`) except to read
  the closure variables the new recursive helper needs.

---

## Implementation Guidance

### Patterns to Follow

See **Research Context → Patterns to Follow** above — the session-lineage indent pattern
(`web/index.html:4534`), the `flExpanded` Set pattern (`:4120`), the lazy per-level fetch
already in the event-list click handler (`:3970-4020`), and the shared icon registry
(`:3731-3761`) are all load-bearing references, not general advice — read the exact lines
before writing new code.

### Code Style

- Vanilla JS/CSS, no build step, no framework — matches every other function in this file.
- Match the file's existing comment style: short paragraphs explaining *why* a non-obvious
  choice was made (see any function in the 4100–5200 range for the convention), not line-by-line
  narration of *what* the code does.
- Reuse `escHtml` for any user-derived string (agent type, description) — every existing row
  renderer in this file does.

### Edge Cases

- An agent with an empty or absent children array (or field) must render exactly as today's
  flat agent row does — no caret, no depth offset beyond the base `22px`.
- A session with `flFocused` set (single-session focus mode) still needs nested-agent expand
  state to work the same as in the unfocused view — `flExpanded`/the new agent-expand Set are
  not reset by focusing.
- Collapsing a mid-tree agent should also visually drop its now-hidden descendants from the
  `rows` array for that render pass (don't leave orphaned rows in the DOM) — matches how
  collapsing a session already drops its agents today (`if (!open) continue;`,
  `web/index.html:4707`).
- Two different sessions cannot have colliding `agent_id`s (see `safeAgentID`,
  `internal/timeline/timeline.go:250-260` — IDs are generated per-agent-file, globally distinct
  in practice), so a single flat expand-state Set keyed by `agent_id` is safe without namespacing
  by session or by parent.

### Testing Requirements

- No new Go tests — this task touches only `web/index.html`. `make test`/`go vet` must still
  pass because they run against the whole branch, including 2a's new Go tests.
- No JS test harness exists in this repo. The actual gate is manual verification against a
  session with real (or hand-constructed, via a test fixture transcript) 3-level agent nesting —
  see 2a's own test fixtures (`internal/timeline/timeline_test.go`,
  `internal/timeline/fleet_test.go`, once 2a lands) as a source of a session ID to test the
  frontend against, since they're built specifically to exercise 3 levels of nesting.

---

## Boundaries

### Files You MUST NOT Touch

Global `never_touch` list (from `.nimble/config.yaml`): `*.lock`,
`extension/package-lock.json`, `.env`, `.env.*`, `*.db`, `*.db-journal`, `*.db-wal`,
`*.db-shm`, `dist/`, `vendor/`, `internal/forecast/MODEL.pdf`, `internal/forecast/archive/**`,
`bench-*.json`, `testdata-bench-*.json`. None of these are relevant to this task's scope.

PRD-specific boundary (`PRD_timeline-feature-merger.md` §11): do not touch
`internal/timeline/burn.go` or the heat-view colour logic / `fs.Burn` wiring in `fleet.go` — out
of scope, recently stabilized. This task doesn't touch Go files at all, but the equivalent
frontend rule applies: do not extend heat styling to nested agent bars (see Success Criteria).

### Files Requiring Review

None of this task's files (`web/index.html`) appear in `.nimble/config.yaml`'s
`require_review` list (`internal/auth/*`, `internal/store/sqlite.go`, `.goreleaser.yml`,
`.github/workflows/*`, `extension/package.json`, `extension/src/*`).

---

## Dependencies

### Upstream Tasks

| Task | What It Provides | Verify Before Starting |
|------|------------------|------------------------|
| 2a | Recursive `attachAgents`/`BuildAgent` parsing on the Go side; a new children field on `FleetAgent` (field name TBD — read `internal/timeline/fleet.go:158-166` once merged, expected near `internal/timeline/fleet.go:158-166` per the PRD's own line reference); `internal/timeline/timeline_test.go`/`fleet_test.go` gain a 3-level nested-agent test fixture | `make test` passes with 2a's new fixture; `grep -n "children" internal/timeline/fleet.go` shows the new field; manually confirm a 3-level test session's `/api/fleet` and `/api/timeline/{id}` responses actually nest (not just flatten with a depth number) |
| 1a | Client-side filter box + tool-name/error chips above the event list, wired into or around `tlRenderEvents` | Re-read `web/index.html:1544-1563` and `tlRenderEvents`/`tlEventRow` for their current line numbers; confirm the filter is applied to the `events` array before mapping to rows, so it composes cleanly with recursive rendering |

### Downstream Impact

Tasks that depend on this one: **3a** (Live/Paused toggle, Wave 3) — depends on this task's
rendering functions (`flRender`, `tlRenderEvents`) keeping stable call signatures so it can gate
their invocation behind a paused flag without further changes to their internals.

**Before starting:** Verify dependencies are complete by checking:
- `git log --oneline feature/timeline-feature-merger -- internal/timeline/fleet.go internal/timeline/timeline.go` shows 2a's merge commit
- `git log --oneline feature/timeline-feature-merger -- web/index.html` shows 1a's merge commit
- `make test && go vet ./...` passes on the branch tip before you start editing

---

## Deliverables

None beyond the files above.

---

## GitHub Context

**Issue:** #TBD (create per `/nimble:run`'s issue-creation step if not already open)
**Feature Issue (Parent):** #TBD
**Branch:** `worktree/timeline-feature-merger-2b`
**Target:** `feature/timeline-feature-merger` (execution mode is `feature-branch` per
`status.json`) — not `main`

---

## Commit Guidelines

Use Conventional Commits:
```
feat(timeline): recursive subagent hierarchy in gantt and event list

Co-Authored-By: Claude <noreply@anthropic.com>
```

Types: feat, fix, refactor, test, docs, chore

---

## Validation Checklist

Before creating PR:
- [ ] All success criteria met
- [ ] Build passes: `make build`
- [ ] Type check passes: n/a (`typecheck: null` in `.nimble/config.yaml`) — rely on `go vet ./...`
- [ ] No `never_touch` files modified
- [ ] Manual in-browser verification against a session with 2+ levels of subagent nesting
- [ ] Fork-curve, heat-view, and liveness manually re-checked against an existing (pre-2b)
      session to confirm no visual change
- [ ] Branch rebased on `feature/timeline-feature-merger`

---

*Generated by NIMBLE Brief Writer*
*PRD: timeline-feature-merger | Task: 2b | Wave: 2*
