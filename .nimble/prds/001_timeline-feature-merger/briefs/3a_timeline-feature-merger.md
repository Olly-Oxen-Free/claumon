# Task Brief: 3a

**Title:** Live/Paused toggle for the gantt
**PRD:** timeline-feature-merger
**Priority:** should
**Complexity:** 4/10
**Model:** sonnet
**Wave:** 3
**Feature Issue:** Not yet created — `status.json.tracker.issue_key` is `null` at brief-generation time; this PRD runs in `execution_mode: feature-branch` (`feature/timeline-feature-merger`) with no GitHub issue tracked per-task in this repo's `.nimble/` state.

---

## Objective

Add a single Live/Paused toggle button to the fleet-gantt controls, next to the existing
1h/3h/1d/3d/1w time-window buttons. While Paused, the frontend stops redrawing the gantt (and
the Timeline event list) in response to the SSE `sessions`/`live` pushes and the 15s/30s polling
fallbacks, so a user can explore a mid-stream gantt without it reflowing under their cursor.
Clicking Live resumes normal refresh and immediately catches up on anything missed. No backend
change — data collection is unaffected either way.

---

## Context

**Parent Feature:** timeline-feature-merger — augmenting claumon's existing Timeline tab (fleet
gantt + event list) with interaction patterns ported from the `agents-observe` plugin, without a
build step and without replacing anything that already works.

claumon's fleet gantt (`web/index.html`, `#tl-graph`) redraws live: every SSE `sessions` push (one
per transcript write — i.e. continuously, for a busy session) and every SSE `live` push (hook
state change) trigger a coalesced re-fetch-and-rerender of the whole gantt. There is no way today
to hold the view still while reading it — the chart can reflow under the reader mid-scroll. This
mirrors a gap `agents-observe` closed with a "Live/Rewind" toggle: Rewind freezes its oscilloscope
view and swaps to a fully explorable historical timeline with drag-to-scroll and position restore
(`research/findings.md:56-57`). claumon's version is deliberately smaller in scope: the gantt is
already historical/scrollable at every window size, so this is a "pin the current view" freeze,
not a from-scratch rewind/scrub engine.

This task is part of **Wave 3** — it is the third task in the PRD's longest dependency chain
(`1a → 2b → 3a → 4a`) and depends on 2b (recursive gantt/event-list rendering) having landed
first. By the time this task executes, waves 1 and 2 (`1a`, `1b`, `2a`, `2b`) will already be
merged into `feature/timeline-feature-merger`, and **all of them touch `web/index.html`**. See
"Known gotcha — line numbers will have moved" below before editing anything.

---

## Research Context

`research/findings.md` (agents-observe comparison, System B, lines 52-57) is the direct source of
this feature's UX shape: agents-observe's Live/Rewind toggle freezes its live timeline and
preserves scroll/position on resume. claumon's existing codebase already has one precedent for a
"paused" concept, worth knowing about specifically so it is **not** confused with this task's
toggle:

- `tlListPinned()` (`web/index.html:3889-3894`) + `tl-paused` indicator
  (`web/index.html:1558`, `:3932-3935`) — an **automatic, scroll-position-derived** pause for the
  Timeline **event list only**: `tlRefresh()` silently skips its own redraw whenever the reader
  has scrolled away from the newest end, and shows a small "paused — scrolled away from the top"
  label. This is unrelated to the new toggle button and must not be repurposed or renamed for it —
  it is a different mechanism (implicit/scroll-derived vs. this task's explicit/button-driven) and
  the two need to compose (see Requirements below), not merge into one flag.

No task-specific research notes exist in the PRD beyond what's summarized above; proceed with the
current-code investigation captured in this brief.

---

## Requirements

1. **New module-level state flag**, e.g. `let flLive = true;` — add it beside the other `fl*`
   gantt-state globals in `web/index.html` (currently `flWindow`, `flEnd`, `flData`, `flExpanded`,
   `flScrollToNow`, `flZoom`, `flPendingScrollLeft`, `flScale`, `flFocused`, `flHeat`, at
   `web/index.html:4117-4142`, ending with `let flHeat = false;` at `:4142`). It is **not**
   persisted to `localStorage` — none of its siblings are (only `flLabelW` is, via
   `claumon.fl.labelw`); pausing resets to Live on reload, matching how `flWindow`/`flZoom`/
   `flHeat` already behave.

2. **New button** in the fleet-gantt controls bar, next to the `FL_WINDOWS` buttons
   (`web/index.html:4082-4088` defines the window list; the controls markup that renders the
   `.fl-windows`/`.fl-pan` button groups is built inside `flRender()` at
   `web/index.html:4448-4473`). Add it as its own `.fl-pan` group (matching the existing pattern
   used for the pan buttons and the heat button), e.g.:
   ```html
   <div class="fl-pan">
     <button class="fl-btn fl-live-btn${flLive ? '' : ' paused'}" data-live-toggle="1"
             title="${flLive ? 'pause redraws to explore freely' : 'resume live redraws'}">
       ${flLive ? '● Live' : '❚❚ Paused'}
     </button>
   </div>
   ```
   Do **not** name the class or id bare `fl-live` / `live` — `.fl-bar.live` already exists
   (`web/index.html:669`, green background for a currently-running session's bar) and `.tl-live`
   already exists (`:827`, the green session-status pill). Reusing either name will visually or
   semantically collide. Use a qualified class (`fl-live-btn`) and a `data-live-toggle` attribute
   for the click-binding selector, following the same `data-*` convention as the sibling controls
   (`data-window`, `data-heat`, `data-pan`, `data-zoom`).

3. **Click handler** — add it inside `flBindControls(host)` (`web/index.html:4794-4857`), which is
   re-called on every `flRender()` (bound at `:4731`) exactly like the existing heat-toggle
   handler it should mirror (`:4805-4818`):
   ```js
   const liveToggle = host.querySelector('.fl-btn[data-live-toggle]');
   if (liveToggle) liveToggle.addEventListener('click', () => {
     const scroller = host.querySelector('.fl-scroll');
     if (scroller) flPendingScrollLeft = scroller.scrollLeft;
     flLive = !flLive;
     if (flLive) { flLoad(); tlRefresh(); } else { flRender(); }
   });
   ```
   Capturing `scroller.scrollLeft` into `flPendingScrollLeft` **before** the state flip is not
   optional — see the gotcha below. Calling `tlRefresh()` on resume catches up the event list too
   (see point 5); `flLoad()` (not `flRefreshSoon()`) forces an immediate re-fetch rather than the
   1.5s-coalesced one, since this is a direct user action, not a burst of SSE pushes.

4. **Gate the redraw functions, not the call sites.** The cleanest, lowest-footprint place to
   freeze redraws is inside the two functions that both the SSE handlers *and* the polling
   fallback timers already funnel through — this covers every current trigger path in two small
   edits instead of three-plus scattered ones:
   - `flRefresh()` (`web/index.html:4392-4396`) — add `if (!flLive) return;` as the first line.
     This function is the single choke point for the gantt: it's called from
     `flRefreshSoon()`'s coalesced `setTimeout` (`:4389`, itself triggered by the SSE `sessions`
     handler at `:3372-3376` and the SSE `live` handler at `:3378-3383`) **and** directly by the
     30s polling fallback (`flRefreshTimer = setInterval(flRefresh, 30000)`, set up in
     `tlActivate()` at `web/index.html:4062`). Gating here freezes all three paths at once.
   - `tlRefresh()` (`web/index.html:3939-3950`) — add a **separate** `if (!flLive) return;` guard
     before the existing `if (!tlSelected || !tlListPinned()) { tlSetPausedFlag(); return; }`
     line. Do not fold this into the existing `tlListPinned()` check — that check drives the
     distinct `tl-paused` scroll-position indicator (see Research Context) and must keep meaning
     "scrolled away from the top", not "manually paused". `tlRefresh()` is likewise the one choke
     point for the event-list side: called directly from the SSE `sessions` handler (`:3374`) and
     from the 15s polling fallback (`tlRefreshTimer = setInterval(tlRefresh, 15000)`, `:4057`).

   This is a deliberate widening beyond the task description's literal wording ("freezes ... the
   'sessions' event handler that calls `tlRefresh()`/`flRefreshSoon()`"). The description names
   that handler only to disambiguate it from the *other*, unrelated `'sessions'` SSE listener at
   `web/index.html:3356-3367` (the dashboard's own session-summary refresh — leave that one
   alone). If only the SSE handler is gated and the 15s/30s polling fallbacks are left unguarded,
   Pause silently stops working every 15-30 seconds regardless of SSE activity, which defeats the
   feature's stated purpose ("explore freely" per PRD §5). Gating the two functions themselves
   is strictly more correct and touches fewer lines than gating every call site individually.

5. **On resume, catch up the event list too.** The task's success criteria only mention the
   gantt, but the single toggle also silences `tlRefresh()` (point 4), so pausing the gantt
   silently pauses the Timeline event list's own live refresh as a side effect. Calling
   `tlRefresh()` in the click handler's Live branch (point 3) is what surfaces any events that
   arrived while paused — without it, the event list would stay stale until the next SSE push
   after resume, which is a regression a reviewer will notice even though it isn't in the written
   criteria.

---

## Success Criteria

Complete ALL criteria before marking task done:

- [ ] Clicking Pause stops gantt redraws on incoming SSE `sessions` events
- [ ] Selection and scroll position are preserved across pause/resume
- [ ] Clicking Live resumes normal refresh behavior, picking up any missed updates
- [ ] `make test` and `go vet ./...` pass

**All criteria must pass before task is complete.**

Manual verification (no UI test harness exists for this repo — `make test`/`go vet` cover the Go
side only, per PRD §7):
1. Open the Timeline tab, select a live/running session so the gantt is actively redrawing.
2. Click Pause. Scroll the gantt horizontally and expand an agent row. Wait through at least one
   30s fallback-timer interval and confirm the view does not reflow, reset scroll, or collapse
   the expanded row.
3. Click Live. Confirm the gantt redraws immediately (not after up to 30s) and the event list for
   the selected session reflects anything that happened while paused.
4. Repeat with the *event list itself* scrolled into history (`tlListPinned()` false) while
   Paused, then Live — confirm the two "paused" concepts don't fight: the scroll-position pause
   should still hold even after Live is clicked, until the reader scrolls back to the top.

---

## Files to Modify

| File | Action | Purpose |
|------|--------|---------|
| `web/index.html` | modify | Add `flLive` state flag, the Live/Paused button + CSS, its click handler in `flBindControls()`, and the `if (!flLive) return;` guards in `flRefresh()`/`tlRefresh()` |

### File Ownership Notes

`web/index.html` is a single shared file every task in this PRD touches. Per the PRD's own
file-overlap mitigation (§6 Risks table), waves are ordered specifically so each frontend task
lands on a settled file — 3a is waved after 1a/1b/2a/2b for exactly this reason. Confine edits to:
the `fl*` state-var block, the `.fl-controls` template inside `flRender()`, `flBindControls()`,
`flRefresh()`, and `tlRefresh()`. Do not touch the event-list filter region (1a), the tabs
array/NIMBLE panel block (1b), or the agent-nesting render logic 2b just landed, beyond what's
needed to place this one button and its two guard clauses.

---

## Implementation Guidance

### Patterns to Follow

- **Mirror the heat-toggle handler exactly** (`web/index.html:4805-4818`) — it is the closest
  existing example of a `.fl-controls` button that flips a boolean `fl*` global, captures
  `scroller.scrollLeft` into `flPendingScrollLeft` first, and chooses between a cheap `flRender()`
  repaint and a full `flLoad()` refetch depending on whether new data is needed.
- **`data-*` attribute selectors, not ids or inline `onclick`**, for every gantt control —
  `data-window`, `data-heat`, `data-pan`, `data-zoom` are the established convention
  (`web/index.html:4795-4856`); use `data-live-toggle` to match.
- **`.on` for the *active/selected* state** (e.g. the currently-selected window button,
  `web/index.html:4449`, `.fl-btn.on` CSS at `:564`) is the wrong convention for "Paused" — `.on`
  reads as "currently selected/active," and a paused gantt is not actively doing anything. Use a
  distinct modifier class (`.paused` suggested above) with its own CSS rule, styled with
  `var(--yellow)` to match the existing paused/stale color language already used by `.tl-paused`
  (`:740`) and `.stale-old` (`:964`) rather than reusing `.fl-btn.on`'s accent-fill treatment.

### Code Style

- Vanilla JS, no framework, no build step — this file has none and the PRD explicitly forbids
  adding one (PRD §3 Non-Goals, §11 Architecture decisions).
- Follow the file's existing comment style: short prose comments above non-obvious logic (see any
  function in the fleet-gantt section) explaining *why*, not restating the code.

### Edge Cases

- **Scroll reset on pause.** `flRender()` rebuilds `#tl-graph`'s entire `innerHTML`
  (`web/index.html:4729`), including a brand-new `.fl-scroll` element, on every call. If
  `flPendingScrollLeft` is not set immediately before calling `flRender()`/`flLoad()`, horizontal
  scroll silently resets to 0 (see the restore logic at `:4743-4761`). This is the single easiest
  way to fail the "scroll position preserved" success criterion — verify it explicitly by testing
  step 2 above at a non-zero scroll offset, not just at the default (rightmost/"now") position.
- **No session selected / no live session.** If `tlSelected` is unset, `tlRefresh()` already
  early-returns before reaching your new guard (existing check, unchanged) — the Pause/Live toggle
  must not throw or behave differently in this state; it should just have nothing to refresh.
- **Toggling Pause with `flHeat` on.** The heat-toggle handler forces a full `flLoad()` because
  heat data (`burn=1` query param) isn't in the already-fetched payload (`:4810-4817`). The
  Live/Paused toggle doesn't need this special case — pausing never needs new data (it renders
  from `flData` already in hand), and resuming already calls `flLoad()` unconditionally per point
  3 above, which re-requests with `burn=1` automatically if `flHeat` is still true (see
  `flLoad()`'s `params.set('burn', '1')` at `:4404`).

### Testing Requirements

This is a frontend-only, vanilla-JS change with no existing JS test harness in this repo (see PRD
§7 Monitoring: "this repo has no UI test harness"). No new automated test is expected. The gate is
`make test` (Go tests, unaffected by this change) and `go vet ./...`, plus the manual verification
steps under Success Criteria. Do not add a Go test for this — there is no Go code involved.

---

## Boundaries

### Files You MUST NOT Touch

From `.nimble/config.yaml` global `never_touch` (none of these overlap `web/index.html`, listed
for completeness): `*.lock`, `extension/package-lock.json`, `.env`, `.env.*`, `*.db`,
`*.db-journal`, `*.db-wal`, `*.db-shm`, `dist/`, `vendor/`, `internal/forecast/MODEL.pdf`,
`internal/forecast/archive/**`, `bench-*.json`, `testdata-bench-*.json`.

PRD-specific (§11): `internal/timeline/burn.go` and the heat-view color logic / `fs.Burn` wiring
in `fleet.go` — out of scope for this whole PRD, recently stabilized. This task does not need Go
changes at all, so this should not come up, but do not "fix" anything there even if noticed.

### Files Requiring Review

From `.nimble/config.yaml` `require_review`: `internal/auth/*`, `internal/store/sqlite.go`,
`.goreleaser.yml`, `.github/workflows/*`, `extension/package.json`, `extension/src/*`. None of
these are touched by this task.

---

## Dependencies

### Upstream Tasks

| Task | What It Provides | Verify Before Starting |
|------|------------------|------------------------|
| 2b | Recursive agent-row nesting in `flRender()`'s row-building loop and the event list's per-level lazy-fetch expansion; must not have regressed fork-curve/heat/liveness rendering | Confirm 2b's PR is merged into `feature/timeline-feature-merger`; re-read `flRender()` (currently `web/index.html:4418+`) and `flBindControls()` (currently `:4794-4857`) in full before editing — nesting changes to the row-building loop very likely shifted every line number below `flRender()`'s start |
| 2a | `FleetAgent`/`agentSpans` children field the recursive nesting (2b) renders — no direct interaction with 3a's toggle, listed for completeness | N/A — 3a only touches frontend redraw-gating, not the Go structures 2a/2b consume |

### Known gotcha — line numbers will have moved

Every `web/index.html:NNNN` anchor in this brief was read directly from the live code on
`feature/timeline-feature-merger` **before** 1a, 1b, 2a, and 2b were implemented (this brief was
generated ahead of wave 1). By the time this task executes, all four will be merged, and **all
four touch `web/index.html`**:
- 1a adds a filter box + chips above the event list.
- 1b adds a new tab entry to the `tabs` array (`:2244` today) and a new panel block.
- 2b restructures the agent-row rendering loop inside `flRender()`.

Any of these can shift every line number that comes after their edit point, including the ones
cited throughout this brief for `FL_WINDOWS`, the `fl*` state-var block, `flRefresh()`,
`tlRefresh()`, the `.fl-controls` template, and `flBindControls()`. **Re-grep for the named
functions/constants (`FL_WINDOWS`, `function flRefresh`, `function tlRefresh`,
`function flBindControls`, `let flHeat`, the `.fl-controls` template literal, the two `'sessions'`
SSE listeners) rather than trusting these line numbers**, and confirm the code still matches the
shape described here (single-level `agents = s.agents || []` will have become recursive after 2b
— that's expected and doesn't change where this task's edits go, just their line numbers).

### Downstream Impact

**4a** (Keyboard nav layer) depends on 3a. It doesn't need to call into `flLive` directly per its
own success criteria (focus-the-filter-box and arrow-key nav in the event list), but confirm the
Live/Paused button, once added, doesn't need a keyboard shortcut reserved for it before 4a picks
its bindings — none is requested in this task or in 4a's brief scope.

---

## Deliverables

None beyond the files above.

---

## GitHub Context

**Issue:** Not yet created (see Feature Issue note above)
**Feature Issue (Parent):** Not yet created
**Branch:** `worktree/timeline-feature-merger-3a`
**Target:** `feature/timeline-feature-merger` (execution_mode: feature-branch, per `status.json` and PRD §7 — one PR per task into the feature branch, then a single final PR to `main` on `Olly-Oxen-Free/claumon`, not the `fabioconcina/claumon` upstream)

---

## Commit Guidelines

Use Conventional Commits:
```
feat(timeline): add live/paused toggle for the fleet gantt

Co-Authored-By: Claude <noreply@anthropic.com>
```

Types: feat, fix, refactor, test, docs, chore

---

## Validation Checklist

Before creating PR:
- [ ] All success criteria met
- [ ] Build passes: `make build`
- [ ] `go vet ./...` passes
- [ ] No `never_touch` files modified
- [ ] Manual verification steps (above) run against a real session with live/running activity
- [ ] Branch rebased on `feature/timeline-feature-merger`

---

*Generated by NIMBLE Brief Writer*
*PRD: timeline-feature-merger | Task: 3a | Wave: 3*
