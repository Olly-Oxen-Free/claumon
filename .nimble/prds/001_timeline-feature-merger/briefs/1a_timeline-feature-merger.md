# Task Brief: 1a

**Title:** Client-side filter/search over the event list
**PRD:** timeline-feature-merger
**Priority:** must
**Complexity:** 3/10
**Model:** sonnet
**Wave:** 1
**Feature Issue:** TBD — `status.json.tracker.issue_key` is `null` (no GitHub issue created yet for this PRD as of this brief). If a PM agent has since created one, use that number instead of leaving this blank in any PR/issue you file.

---

## Objective

Add a client-side filter (free-text box + tool-name chip + error-only chip) above claumon's
Timeline event list, so a user can narrow a long session's event list to a specific tool call,
error, or keyword without scrolling. This is a pure frontend feature over data claumon already
fetches — no backend, no new endpoint, no use of the existing (wrong-shape) full-text search API.

---

## Context

**Parent Feature:** Timeline & Event Viewer Feature Merger (`timeline-feature-merger`, this repo's
first NIMBLE PRD; GitHub issue not yet created — see Feature Issue above).

claumon's Timeline tab has two coupled panels: a fleet gantt (`flRender`) and, beneath it, a full
per-session event list (`tlRenderEvents`/`tlEventRow`) showing prompts, messages, tool calls,
subagent spawns, and compaction boundaries. Today that event list has no way to narrow itself —
a long session forces manual scrolling to find one tool call or error. A comparative research pass
against the `agents-observe` plugin (2026-08-20, see PRD `## Research Findings` / `research/
findings.md`) found this filter/search UX is the single lowest-risk, highest-value gap to close,
because the event-list side of `web/index.html` (unlike the actively-iterated gantt) is
comparatively stable.

An `/api/sessions/search` endpoint already exists server-side
(`internal/parser/search.go`, wired in `internal/server/handlers.go`) but it full-text-scans the
**entire `~/.claude` transcript corpus** across all sessions, with no session-scoping parameter,
matching only message text/thinking — the wrong shape for narrowing one already-open session's
event list. It is explicitly out of scope for this task (see Boundaries below); do not wire it in.

This task is part of **Wave 1** of a 4-wave plan — it runs in parallel with 1b (NIMBLE panel,
disjoint region of the same file) and 2a (Go-side recursive hierarchy parsing, different files).
Wave 2's task 2b (gantt/event-list rendering for recursive hierarchy) depends on this task landing
first, specifically so it lands on a settled `web/index.html` rather than colliding with these
edits.

---

## Research Context

From `research/findings.md` (System A — claumon current state):

### Patterns to Follow

- **Event list source of truth:** `tlRenderEvents(tl, sessionId)` (`web/index.html`) renders
  `tl.events` (the array returned by `GET /api/timeline/{id}`) via `tlEventRow(e, sessionId)` for
  each event. This array is exactly the data to filter — do not re-fetch or introduce a second data
  source.
- **Existing chip/filter-toggle idiom:** the Memories tab (`renderMemoriesFilters`,
  `web/index.html:2329-2355`) and Graph tab both use a `.filter-btn` / `.filter-btn.active` toggle
  pattern with a `Set` tracking active filters (`activeFilters`, `web/index.html:2271`). Reuse this
  visual/interaction idiom for the error-only chip rather than inventing new chip styling.
- **Existing text-input idiom:** `#memory-search` (`web/index.html:1523`, styled at
  `.memories-search input`, `web/index.html:831-841`) is the established plain `<input>` +
  `placeholder` pattern for a live-narrowing search box. Match this styling for the new event
  filter input.
- **Icon registry:** `EV_ICONS` (`web/index.html:3731-3745`) is the canonical list of known tool
  names (`Bash`, `Read`, `Write`, `Edit`, `MultiEdit`, `NotebookEdit`, `Glob`, `Grep`, `WebSearch`,
  `WebFetch`, `Task`, `TodoWrite`, `ToolSearch`) plus a fallback for unmapped/MCP tools
  (`server__tool` naming, `web/index.html:3752-3757`). Useful as a reference for what tool-name
  values look like, but the tool-name filter chip must be populated **dynamically from the current
  session's actual events**, not hardcoded from this registry (a session may use MCP tools not in
  the list, or use only a subset).

### Known Issues to Address

- **Naming collision risk:** `#tl-filterchip` (`web/index.html:1554`), the `.tl-chip`/`.tl-chip-
  name`/`.tl-chip-x` CSS classes (`web/index.html:721-733`), and the function
  `tlRenderFilterChip()` (`web/index.html:4224-4241`) **already exist** — but they belong to an
  unrelated, already-shipped feature: the gantt's session-focus filter (`flFocused`, click a gantt
  bar to focus on one session, shown as a removable chip in that same `.tl-listbar` header). Do
  **not** reuse the ID `tl-filterchip`, the function name `tlRenderFilterChip`, or repurpose that
  existing chip's DOM node for this task's filter UI. Use new, distinct IDs (suggested below) for
  the free-text input, tool-name control, and error-only chip. Reusing the `.filter-btn` *CSS
  class* (not the `.tl-chip` class, and not any of the reserved IDs) for the error-only chip is
  fine and encouraged for visual consistency with Memories/Graph.
- **Subagent-expanded sublists are a separate fetch, not part of `tl.events`:** clicking a
  `subagent` row lazily fetches that agent's own events via
  `GET /api/timeline/{sessionId}/agents/{agentId}` (`web/index.html:4010-4019`) into a *different*
  container (`#tl-agent-{agentId}`, filled via `tlEventRow` calls at `web/index.html:4016`, not via
  `tlRenderEvents`). The task description and success criteria scope this task to
  **the top-level list rendered by `tlRenderEvents`** — filtering the lazily-fetched subagent
  sublists is explicitly out of scope for 1a (not required by any success criterion below); do not
  spend effort wiring the filter into that second code path.

### Recommended Approach

- Filter purely in-memory in JavaScript, no new fetch, no debounce needed at this data size
  (sessions are at most a few hundred events).
- Combine free-text + tool-name + error-only as **AND** (per task description: "combine as AND").
- Free text matches case-insensitively against `Title`, `Detail`, `Full`, and `Output` (per task
  description's explicit field list — do not add `Kind` to the free-text match surface, it isn't
  human-readable search text).

### Dependencies

No file dependencies on other tasks — 1a has `depends_on: []` in `tasks.yaml` and touches only
`web/index.html`, no shared types/utilities from another in-flight task. No new library
dependencies (vanilla JS, per PRD's no-build-step / no-framework constraint).

---

## Requirements

Implement, in `web/index.html`, above the existing `#tl-list` event list:

1. **Free-text filter input** — a plain `<input type="text">` with a placeholder (e.g. `"Filter
   events…"`). Typing narrows the visible rows live (on `input` event, not requiring Enter).
   Matches case-insensitively against each event's `title`, `detail`, `full`, and `output` fields.
2. **Tool-name filter control** — narrows the list to events whose `title` equals a chosen tool
   name. Populate its option set **dynamically** from the distinct `title` values of events with
   `kind === 'tool'` in the *currently loaded* session's `tl.events` — not from a hardcoded list —
   so it always reflects what that session actually called. Include an "All tools" / empty default
   that clears this filter.
3. **Error-only chip** — a toggle button/chip (recommended: reuse the existing `.filter-btn` /
   `.filter-btn.active` pattern) that, when active, narrows the list to events where
   `is_error` is truthy.
4. All three filters combine as **AND**: an event must satisfy the free text (if non-empty), the
   selected tool name (if any), and the error-only toggle (if active) simultaneously to remain
   visible.
5. Filtering must operate on `tl.events` (the array already held as `tlCurrentTimeline.events`)
   client-side — no new fetch, and do **not** call or wire in `/api/sessions/search`.
6. When the filtered result is empty, show the same visual "no events" empty state the list
   already uses elsewhere (reuse the `.tl-empty` CSS class inside `#tl-list`; wording may differ
   from the "no recorded activity" / "pick a session" messages already in the code, e.g. "No events
   match your filter," but it must be the same `.tl-empty` block styling, not a broken/blank
   layout).
7. Filters must keep working correctly with the existing sort-direction toggle (`tlSort`,
   `#tl-sortdir`, `web/index.html:4026-4039`) and with the periodic auto-refresh (`tlRefresh`,
   `web/index.html:3939-3950`) — i.e., filtering must be applied every time the list is
   (re-)rendered, not just once on initial load.

---

## Success Criteria

Complete ALL criteria before marking task done:

- [ ] Typing in the filter box narrows visible event-list rows live, matching against
      Title/Detail/Full/Output client-side
- [ ] Tool-name filter chip and error-only (IsError) filter chip both work and combine with free
      text as AND
- [ ] Filtering to zero results shows the existing "no events" empty state, not a broken layout
- [ ] `make test` and `go vet` pass

**All criteria must pass before task is complete.**

(No UI test harness exists in this repo — verify these manually in-browser against a real session
with a mix of tool calls, errors, and non-tool events, per the PRD's Rollout Plan / Monitoring
section. `make test`/`go vet` cover only the Go side and are expected to be unaffected by a
frontend-only change; run them anyway to confirm no accidental breakage.)

---

## Files to Modify

| File | Action | Purpose |
|------|--------|---------|
| `web/index.html` | modify | Add filter input, tool-name control, error-only chip to the Timeline event-list header; add matching CSS; add JS filter-application logic wired into `tlRenderEvents`/`tlRefresh`/`tlLoad`. |

### File Ownership Notes

`web/index.html` is being edited concurrently by task 1b (NIMBLE panel — new `#tab-nimble` tab,
confined to the `const tabs = [...]` array at `web/index.html:2244`, the activation switch at
`:2251-2252`, and a new self-contained panel block). Per the PRD, 1a and 1b are scoped to disjoint
regions of this file to avoid merge conflicts:

- **1a's region:** the `<div class="tab-content" id="tab-timeline">` block (currently
  `web/index.html:1543-1563`, specifically the `.tl-listbar` div at `:1552-1559`), the CSS block
  around `:719-742`, and the JS functions/listeners in the `// --- Timeline tab ---` section
  (currently `web/index.html:3702-4063`).
- **1b's region:** the tabs array/switch (`:2244`, `:2251-2252`) and a new panel block elsewhere.

Do not touch the tabs array, the keyboard-shortcut handler (`web/index.html:2234-2268` — the `f`-
key binding for this filter box is task 4a's job, not this one), or anything outside the Timeline
tab's own markup/CSS/JS.

Line numbers above were verified live on `feature/timeline-feature-merger` at the time this brief
was written; if 1b or another task lands first and shifts them, use the anchors (`.tl-listbar`,
`#tl-list`, `function tlRenderEvents`, `const tabs = [...]`) to relocate, not the raw numbers.

---

## Implementation Guidance

### Patterns to Follow

- **HTML placement** — add the new filter controls inside (or immediately adjacent to) the
  existing `.tl-listbar` div:

  ```html
  <!-- web/index.html:1552-1559, current state -->
  <div class="tl-listbar">
    <span class="tl-listtitle">Events</span>
    <span id="tl-filterchip"></span>
    <button class="fl-btn tl-sortdir" id="tl-sortdir" title="newest first — click to reverse">
      <svg class="tl-arrow" viewBox="0 0 16 16"><use href="#ic-arrow-down"/></svg>
    </button>
    <span class="tl-paused" id="tl-paused">paused — scrolled away from the top</span>
  </div>
  ```

  `#tl-filterchip` here is the **existing, unrelated** gantt session-focus chip (rendered by
  `tlRenderFilterChip()`, `web/index.html:4224-4241` — do not modify that function). Add new,
  distinctly-named elements alongside it, e.g.:

  ```html
  <div class="tl-eventfilter">
    <input type="text" id="tl-filter-text" class="tl-filter-input" placeholder="Filter events…" />
    <select id="tl-filter-tool" class="tl-filter-tool"><option value="">All tools</option></select>
    <button class="filter-btn" id="tl-filter-errors" title="show only events that errored">Errors only</button>
  </div>
  ```

  (Element IDs, tag choice for the tool-name control, and exact wording are suggestions, not
  mandates — keep them distinct from `tl-filterchip`/`tl-chip*`/`tlRenderFilterChip` and keep the
  markup inside the Timeline tab's own region as described in File Ownership Notes.)

- **CSS** — add new rules near the existing `.tl-listbar`/`.tl-chip` block
  (`web/index.html:719-733`). Reuse `var(--bg-surface)`, `var(--border)`, `var(--text)`,
  `var(--text-dim)`, `var(--accent)` custom properties already used throughout this stylesheet
  (see `.memories-search input`, `web/index.html:831-840`, and `.filter-btn`,
  `web/index.html:921-932`, for the exact tokens/values to match). Do not introduce new color
  literals.

- **JS state** — add filter state near the other Timeline-tab module-level `let`/`const`
  declarations (`web/index.html:3706-3712`, alongside `tlSelected`, `tlCurrentTimeline`,
  `tlExpanded`), e.g. `tlFilterText`, `tlFilterTool`, `tlFilterErrorsOnly`.

- **Filtering hook point** — `tlRenderEvents(tl, sessionId)` (`web/index.html:3876-3884`) is the
  single function that turns `tl.events` into rendered rows, and it is called from both `tlLoad`
  (`web/index.html:3797`) and `tlRefresh` (`web/index.html:3946`) and `tl-sortdir`'s click handler
  (`web/index.html:4032`). Apply the filter inside (or immediately before) this function so all
  three call sites stay correctly filtered without duplicating logic:

  ```js
  function tlRenderEvents(tl, sessionId) {
    const list = document.getElementById('tl-list');
    if (!tl.events || !tl.events.length) {
      list.innerHTML = '<div class="tl-empty">That session has no recorded activity.</div>';
      return;
    }
    let events = tlSort === 'desc' ? tl.events.slice().reverse() : tl.events;
    events = tlApplyFilter(events); // new
    if (!events.length) {
      list.innerHTML = '<div class="tl-empty">No events match your filter.</div>'; // new
      return;
    }
    list.innerHTML = events.map(e => tlEventRow(e, sessionId)).join('');
  }
  ```

- **Tool-name option population** — populate the tool-name control's options once per session
  load, from `tl.events`, at the point `tlCurrentTimeline` is set in `tlLoad`
  (`web/index.html:3794-3797`), not inside `tlRenderEvents` (which runs on every refresh/sort
  toggle and would otherwise thrash the `<select>`'s open state / selection):

  ```js
  fetch('/api/timeline/' + encodeURIComponent(sessionId))
    .then(r => { if (!r.ok) throw new Error('not found'); return r.json(); })
    .then(tl => {
      tlCurrentTimeline = tl;
      tlPopulateToolFilter(tl.events); // new — before tlRenderEvents
      tlRenderTotals(tl.totals);
      tlRenderEvents(tl, sessionId);
      ...
  ```

  `tlPopulateToolFilter` should derive the option set via something like
  `[...new Set(events.filter(e => e.kind === 'tool').map(e => e.title))].sort()`, preserve the
  previously-selected tool if it still exists in the new session, and fall back to "All tools" if
  it doesn't.

- **Listener wiring** — add `input`/`change`/`click` listeners for the three new controls near the
  existing Timeline-tab listeners (`web/index.html:4022-4044`, e.g. right after the `#tl-sortdir`
  click listener), each updating the relevant `tlFilter*` state variable and then calling
  `tlRenderEvents(tlCurrentTimeline, tlSelected)` if `tlCurrentTimeline` is set — the same pattern
  `#tl-sortdir`'s own listener already uses (`web/index.html:4032`).

### Code Style

- No semicolon-per-statement inconsistency, no new global names beyond the module-level `tl`-
  prefixed pattern already established (`tlLoad`, `tlRefresh`, `tlSelected`, etc.) — prefix any new
  function/variable with `tl` to match.
- Use `escHtml()` (`web/index.html:1643`) for any user-influenced string interpolated into
  `innerHTML` (not strictly needed here since filter values only ever *select/exclude* existing
  rows rather than being echoed into markup, but keep the convention in mind if you add e.g. a
  "showing N of M events" counter).
- Match existing comment style: short paragraph comments explaining *why*, placed directly above
  the function/block they describe (see the comment above `tlListPinned`, `web/index.html:3886-
  3888`, as a representative example).

### Edge Cases

- Empty free-text input, no tool selected, error-only off → all events show (current behavior,
  unchanged).
- Switching sessions (`tlLoad`) must repopulate the tool-name option list from the new session's
  events; a previously-selected tool name that doesn't exist in the new session must not leave the
  filter silently matching zero events forever — reset to "All tools" in that case.
- A session with zero `kind === 'tool'` events should still render a usable (effectively empty/
  "All tools only") tool-name control, not throw.
- Filtering must not break the existing sort toggle, auto-refresh, or expandable-row / subagent-
  row click handling (`web/index.html:3970-4020`) for whatever rows remain visible after filtering.
- Whether filter state persists across a session switch (`tlLoad`) or resets is not specified by
  the success criteria — recommended: **do not reset** free text / tool selection / error-only on
  session switch (only repopulate the tool-name option list, per the edge case above), since a
  user filtering for "errors only" while triaging is more likely to want that to persist across
  sessions they check next. This is a judgment call, not a hard requirement.

### Testing Requirements

This repo has no frontend/UI test harness (`make test` runs `go test ./...`, Go-only). Verify
manually in-browser:

1. Open the Timeline tab against a session with a mix of tool calls (including at least one error)
   and non-tool events (prompts/messages).
2. Type a keyword present only in one tool call's `detail`/`full`/`output` — confirm only that row
   (and any others genuinely matching) remain.
3. Select a specific tool from the tool-name control — confirm only that tool's rows remain, AND'd
   with any active free text.
4. Toggle "Errors only" — confirm only `is_error` rows remain, AND'd with the other two filters.
5. Combine all three to a set that matches nothing — confirm the `.tl-empty` "no events match"
   state renders cleanly, no console errors, no layout break.
6. With a filter active, click `#tl-sortdir` to reverse order, and let `tlRefresh` fire (or trigger
   it manually) — confirm the filter stays applied both times.
7. Run `make test` and `go vet ./...` — confirm both still pass (this task shouldn't touch Go code
   at all, so this is a regression check, not expected to find anything).

Coverage Expectations section omitted — this is not a test task (task type is a UI feature, title/
files_affected don't match test-task detection criteria).

---

## Deliverables

None beyond the files above (inferred — `tasks.yaml` task 1a has no `deliverables` field; this was
not captured at interview, so this line is inferred, not verified against a human-confirmed
artifact list).

---

## Boundaries

### Files You MUST NOT Touch

From `.nimble/config.yaml` global `never_touch` (applies repo-wide): `*.lock`,
`extension/package-lock.json`, `.env`, `.env.*`, `*.db`, `*.db-journal`, `*.db-wal`, `*.db-shm`,
`dist/`, `vendor/`, `internal/forecast/MODEL.pdf`, `internal/forecast/archive/**`, `bench-*.json`,
`testdata-bench-*.json`. None of these are anywhere near this task's scope.

**Task-specific (from the PRD, R1's explicit instruction and §6/§11):**

- `internal/parser/search.go` — do not touch; this task deliberately does not use
  `/api/sessions/search` (wrong shape — scans the whole `~/.claude` corpus, no session-scoping
  param).
- `internal/server/handlers.go` — do not touch; no backend change of any kind for this task, it is
  frontend-only.
- `internal/timeline/burn.go`, `fs.Burn` wiring in `internal/timeline/fleet.go` — out of scope,
  recently stabilized; not relevant to this task's files anyway (this task only touches
  `web/index.html`), listed here for completeness per the PRD's global agent-boundaries section.
- The `const tabs = [...]` array (`web/index.html:2244`) and activation switch
  (`web/index.html:2251-2252`) — reserved for task 1b; do not edit even incidentally.
- The keyboard-shortcut `document.addEventListener('keydown', ...)` handler
  (`web/index.html:2234-2268`) — the `f`-key focus shortcut for this filter box is task 4a's scope,
  not 1a's. Do not add a keybinding in this task.
- `#tl-filterchip`, `.tl-chip`/`.tl-chip-name`/`.tl-chip-x` CSS classes, and
  `tlRenderFilterChip()` (`web/index.html:4224-4241`) — existing, unrelated gantt session-focus
  feature; do not repurpose or modify.

### Files Requiring Review

From `.nimble/config.yaml`: `internal/auth/*`, `internal/store/sqlite.go`, `.goreleaser.yml`,
`.github/workflows/*`, `extension/package.json`, `extension/src/*`. This task does not touch any of
these — no review-gate concern for 1a.

---

## Dependencies

### Upstream Tasks

None — `depends_on: []` in `tasks.yaml`. This task can start immediately.

### Downstream Impact

**2b** (Recursive subagent hierarchy — gantt and event-list rendering) depends on this task
(`depends_on: ["1a", "2a"]`), specifically so it lands on a `web/index.html` where the event-list
header region is already settled rather than colliding with these edits. Land this task cleanly —
avoid leaving the `.tl-listbar` region or the `tlRenderEvents` function in a half-finished state at
merge time, since 2b will edit `tlEventRow`'s subagent-expansion logic next and needs a stable
starting point.

**Before starting:** No upstream dependency to verify — this is a Wave 1 task with no
`depends_on`.

---

## GitHub Context

**Issue:** TBD — no issue created yet for this task (see Feature Issue note above).
**Feature Issue (Parent):** TBD — `status.json.tracker.issue_key` is `null`.
**Branch:** `worktree/timeline-feature-merger-1a`
**Target:** `feature/timeline-feature-merger` (per `status.json`: `execution_mode:
"feature-branch"`, `feature_branch: "feature/timeline-feature-merger"` — PRs land on this branch,
not `main`, per the PRD's Rollout Plan).

---

## Commit Guidelines

Use Conventional Commits:
```
feat(timeline): add client-side filter/search over the event list

Co-Authored-By: Claude <noreply@anthropic.com>
```

Types: feat, fix, refactor, test, docs, chore

---

## Validation Checklist

Before creating PR:
- [ ] All success criteria met
- [ ] Build passes: `make build`
- [ ] Type check: N/A (`.nimble/config.yaml` has `typecheck: null` for this project — Go project,
      no separate typecheck step; `go vet ./...` and `make build` are the relevant static checks)
- [ ] `go vet ./...` passes
- [ ] `make test` passes
- [ ] No `never_touch` files modified
- [ ] `internal/parser/search.go` and `internal/server/handlers.go` untouched
- [ ] `web/index.html` edits confined to the Timeline tab's own markup/CSS/JS region (tabs array,
      keyboard handler, and gantt session-focus-chip code untouched)
- [ ] Manual in-browser verification per Testing Requirements above (no automated UI test harness
      exists in this repo)
- [ ] Branch rebased on `feature/timeline-feature-merger`

---

*Generated by NIMBLE Brief Writer*
*PRD: timeline-feature-merger | Task: 1a | Wave: 1*
