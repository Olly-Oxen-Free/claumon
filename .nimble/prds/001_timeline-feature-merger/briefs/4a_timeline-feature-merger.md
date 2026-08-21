# Task Brief: 4a

**Title:** Keyboard nav layer across timeline regions
**PRD:** timeline-feature-merger
**Priority:** could
**Complexity:** 4/10
**Model:** sonnet
**Wave:** 4
**Feature Issue:** N/A — no GitHub issue tracker configured for this PRD (`status.json.tracker.issue_key` is `null`; tracked as NIMBLE plan item `plan_1787266927737_stpt6i` only)

---

## Objective

Add a keyboard-driven navigation layer to claumon's Timeline tab: press `f` to focus the
event-list filter box added by task 1a, and use arrow keys to move a keyboard-focus cursor
through event-list rows when the list has focus. This closes R5 (Keyboard nav layer) and is
the accessibility improvement called out for this PRD — there is no separate a11y requirement
beyond it.

---

## Context

**Parent Feature:** Timeline & Event Viewer Feature Merger (`timeline-feature-merger`) — no
GitHub issue configured; see `.nimble/prds/001_timeline-feature-merger/PRD_timeline-feature-merger.md`.

claumon's Timeline tab (fleet gantt + event list, `web/index.html`, tab id `tab-timeline`)
currently has no keyboard-driven interaction beyond the app-global shortcuts already wired at
`web/index.html:2234-2268` (digit keys 1-4 switch tabs, `Escape` closes the session-detail
overlay, `/` focuses Memories-tab search). This PRD ports the highest-value interaction
patterns from the `agents-observe` plugin into claumon's existing vanilla-JS/no-build-step
architecture; `agents-observe` implements a full region-jump layer (`/`/`s` search, `a`
agents, `f` filters, `b` sidebar, `e` event stream, plus arrow-key nav per region — see
`research/findings.md:68-69`). This task is the claumon-native, minimum-viable slice of that
pattern: a working `f` shortcut and event-list arrow nav are the bar; region-jump across all
four zones (gantt / event-list / filter-box / NIMBLE-panel) is explicitly a stretch goal, not
a blocking requirement.

This task is part of **Wave 4** — the final wave, and a wave of one. It is the last link in
the PRD's longest dependency chain (`1a → 2b → 3a → 4a`, `execution_plan.yaml`). It depends on
1a (event-list filter box) and 3a (Live/Paused gantt toggle) having landed first.

**Dependency status at brief-writing time:** neither 1a nor 3a has landed on
`feature/timeline-feature-merger` yet — grepping the live file for `tlFilter`, `tl-filter`,
`flPause`, or a Live/Paused toggle button returns nothing as of this brief. The filter-box
element ID, the filter input's exact selector, and any Live/Paused toggle button ID are
**not yet real** and are inferred below from this file's existing naming conventions
(`tl-` prefix, kebab-case IDs, e.g. `tl-list`, `tl-session`, `tl-live`, `tl-paused`,
`tl-filterchip` — the last of which is an *existing, currently-empty* `<span>` at
`web/index.html:1554` inside `.tl-listbar`, most likely where 1a will mount its filter
`<input>`). **Re-verify the actual selector 1a lands with before wiring the `f` shortcut** —
do not assume the guessed ID below is correct; `grep -n "tl-filter" web/index.html` after 1a
merges is the fastest check.

---

## Research Context

### Patterns to Follow

- **Existing global keydown handler** (`web/index.html:2234-2268`) is the one and only place
  app-wide keyboard shortcuts are wired today. **Extend this handler; do not add a second
  `document.addEventListener('keydown', ...)` listener** — a second listener doubles dispatch
  overhead for every keystroke app-wide and makes the "ignore shortcuts while typing" guard
  (`web/index.html:2242`) easy to accidentally bypass in the new listener.
- **"Ignore shortcuts when typing" guard** (`web/index.html:2242`):
  `if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;` — this guard
  sits *after* the `Escape` check but *before* the digit-key tab-switch and `/` blocks. Any new
  `f`-key or arrow-key logic for the filter-box shortcut must sit after this guard (so typing
  the letter `f` into any input, including 1a's new filter box itself, never re-triggers
  focus). The event-list arrow-key handling is the one exception: it must work whether or not
  `tl-list` itself is an `INPUT`/`TEXTAREA` (it won't be — see below), so it is unaffected by
  this guard either way.
- **Tab-switch pattern** (`web/index.html:2244-2254`) — reference implementation for "how this
  codebase toggles `.active` on `.tab` / `.tab-content` pairs and calls `tlActivate()` on
  activation." Reuse this exact pattern if the stretch goal adds a Timeline-tab-activation
  step before focusing a region (e.g. jumping to the NIMBLE panel would need to switch to
  `#tab-nimble` the same way the `/` handler switches to `#tab-memories` at
  `web/index.html:2258-2265`).
- **`/` handler structure** (`web/index.html:2256-2267`) is the direct template for the `f`
  handler: check `e.key`, `e.preventDefault()`, ensure the Timeline tab is active (mirroring
  the memories-tab-activation guard), then `.focus()` the target element. Timeline tab id is
  `tab-timeline`, tab button selector is `.tab[data-tab="timeline"]` (confirmed at
  `web/index.html:2249` / `1591-1600`).
- **Event-list row markup** (`tlEventRow`, `web/index.html:3829-3870`) — rows are
  `<div class="tl-row {kind} d{depth} [expandable] [subagent] [err]" data-seq="{seq}">` inside
  `<div class="tl-list" id="tl-list">`. **Rows currently have no `tabindex` and are not
  natively focusable** — arrow-key nav must add its own keyboard-focus tracking (e.g. a
  `tl-kbd-focus` CSS class toggled onto one row at a time) rather than relying on native
  `:focus`/tab order, since the rows are plain `<div>`s with a click handler
  (`web/index.html:3970`), not buttons or `tabindex="0"` elements. Give `#tl-list` itself
  `tabindex="0"` (or a first-row focus target) so it can receive the arrow-key events at all;
  check `tlSort` (`web/index.html:3874`, `'desc'` by default) when deciding which direction
  ArrowDown/ArrowUp should move — the DOM order already reflects the current sort, so moving
  visually down should always mean "next `.tl-row` in DOM order" regardless of `tlSort`.
- **Existing `:focus` styling precedent**: `.memories-search input:focus { border-color:
  var(--accent); }` (`web/index.html:840`) — follow this for the new keyboard-focus indicator
  on `.tl-row` (a class-driven equivalent, e.g. `.tl-row.tl-kbd-focus { background:
  var(--bg-hover); outline: 1px solid var(--accent); }` or similar, consistent with how
  `.tl-row.open` already gets `background:var(--bg-hover)` at `web/index.html:762`).

### Known Issues to Address

- No known issues from research specific to this task beyond the dependency-timing note above
  (1a/3a not yet landed — re-verify selectors, do not hardcode guessed IDs without checking).

### Recommended Approach

- Vanilla JS only, one extended listener, no new dependency — consistent with every other task
  in this PRD and the PRD's explicit non-goal of "no new build step, bundler, or frontend
  framework."
- Scope the arrow-key handler to only act when Timeline tab is active AND focus is inside
  `#tl-list` (or a tracked keyboard-focus state), so ArrowUp/ArrowDown do not hijack scrolling
  or navigation on other tabs (Dashboard/Memories/Graph, and NIMBLE panel if 1b has landed).
- `agents-observe`'s full region-jump set (`research/findings.md:68-69`) is a useful reference
  for shortcut-key choices *if* the stretch goal is attempted, but its keys (`s`, `a`, `b`,
  `e`) are not required — this PRD only mandates `f` (already decided, do not re-litigate) and
  leaves any additional region-jump keys to implementer judgment, provided none collide with
  `1`-`4` (tab switch), `/` (Memories search), or `Escape`.

### Dependencies

**File Dependencies:**
- `web/index.html` (1a) — adds the filter-box `<input>` this task's `f` shortcut must focus.
  Exact ID/selector not yet known; re-verify via `grep -n "tl-filter\|filter" web/index.html`
  before wiring.
- `web/index.html` (3a) — adds the Live/Paused toggle button near the timeline controls
  (`.tl-controls`, `web/index.html:1545-1548`). Only relevant to 4a if the stretch goal
  region-jump set includes a shortcut for the pause toggle; not required for the minimum bar.

**Library Dependencies:**
- None.

---

## Requirements

From PRD §4, R5 (Could Have):

> At minimum: a shortcut to focus the filter box and arrow-key navigation within the event
> list; stretch: region-jump across gantt/list/filters/NIMBLE panel.

Concretely for this task:

1. **`f` → focus filter box.** Pressing `f` anywhere in the Timeline tab (and not while typing
   in any input/textarea, per the existing guard) switches to the Timeline tab if not already
   active and focuses 1a's event-list filter `<input>`.
2. **Arrow-key nav in event list.** When keyboard focus is within `#tl-list` (or a tracked
   keyboard-focus row exists), `ArrowDown`/`ArrowUp` move a visible focus indicator to the
   next/previous `.tl-row` in DOM order. Reasonable behavior at list boundaries (no-op or clamp
   at first/last row — do not wrap or throw).
3. **No collisions.** `Escape` (session-detail close, `web/index.html:2235-2239`) and `/`
   (Memories search, `web/index.html:2256-2267`) must continue to work exactly as before —
   verify by manually triggering both after this task's changes land.
4. **Stretch (optional, cut first under time pressure):** additional region-jump shortcuts
   moving focus between gantt (`#tl-graph`), event list (`#tl-list`), filter box (1a), and
   NIMBLE panel (1b, if landed) — vanilla JS, no new dependency, no key collision with the
   above.

---

## Success Criteria

Complete ALL criteria before marking task done (verbatim from `tasks.yaml`):

- [ ] Pressing `f` focuses the Timeline filter box from anywhere in the Timeline tab
- [ ] Arrow keys navigate rows within the event list when it has focus
- [ ] No conflict with existing bindings: Escape (modal close) and `/` (Memories search) are untouched
- [ ] `make test` and `go vet` pass

**All criteria must pass before task is complete.**

---

## Files to Modify

| File | Action | Purpose |
|------|--------|---------|
| `web/index.html` | modify | Extend the global keydown handler (`web/index.html:2234-2268`) with an `f` shortcut and event-list arrow-key navigation; add supporting CSS for a keyboard-focus indicator on `.tl-row`; add `tabindex` to the event-list focus target as needed |

### File Ownership Notes

`web/index.html` is shared across every task in this PRD (1a, 1b, 2b, 3a all touch it). By
Wave 4, 1a and 3a should already be merged into the feature branch, so this task lands on a
settled file per the PRD's stated mitigation (§6 risk table: "2b/3a/4a waved after 1a so each
lands on a settled file"). Confirm 1a and 3a are actually merged into
`feature/timeline-feature-merger` before starting (see Dependencies section below) — do not
start against a stale local checkout.

Confine edits to: the keydown handler block (`~2234-2268`, exact lines will shift once 1a/3a
land — re-locate via `grep -n "keydown" web/index.html`), and a small, self-contained CSS rule
for the keyboard-focus indicator. Do not touch the gantt-rendering (`flRender`,
`flRefreshSoon`) or event-list-rendering (`tlRenderEvents`, `tlEventRow`) function bodies
beyond what's strictly needed to add `tabindex`/focus-class support — those are 2a/2b/3a
territory and were recently stabilized.

---

## Implementation Guidance

### Patterns to Follow

See **Research Context → Patterns to Follow** above — it is the authoritative, file:line-
anchored guidance for this task. Summary:

- Extend the existing single `keydown` listener at `web/index.html:2234`; don't add a second one.
- Model the `f` handler directly on the `/` handler (`web/index.html:2256-2267`): tab-activation
  guard, then `.focus()`.
- Track keyboard-focus row via a CSS class (e.g. `tl-kbd-focus`), not native `:focus`, since
  `.tl-row` elements are plain unfocusable `<div>`s today.
- Respect current `tlSort` state when reasoning about "next"/"previous" — but note DOM order
  already reflects the active sort, so simple `nextElementSibling`/`previousElementSibling`
  traversal over `.tl-row` is sufficient without re-deriving sort direction.

### Code Style

- Match existing vanilla-JS style in this `<script>` block: no semicolon-omission style
  changes, no arrow-function-vs-function-declaration churn — follow whatever the immediately
  surrounding code already uses (the keydown handler uses `e =>` arrow-function callbacks;
  keep that).
- No new npm/JS dependency of any kind — this file has none and the PRD explicitly forbids
  adding one for this task.
- CSS additions go alongside the existing `.tl-row*` rule block (`web/index.html:748-815`) to
  keep related styling co-located, matching the file's existing convention of grouping rules
  by component prefix.

### Edge Cases

- **Filter box not yet focused/visible** (e.g. Timeline tab never opened this session): `f`
  handler must switch to the Timeline tab first, exactly as `/` does for Memories
  (`web/index.html:2258-2265`), before calling `.focus()` — a `.focus()` call on a hidden
  element in an inactive `.tab-content` is a silent no-op in most browsers and must not be the
  only step.
- **Empty event list**: `tlRenderEvents` (`web/index.html:3876-3884`) renders a
  `.tl-empty` div with no `.tl-row` children when a session has no events. Arrow-key handler
  must no-op gracefully (no error) when `#tl-list` contains no `.tl-row`.
- **List boundaries**: pressing ArrowUp at the first row or ArrowDown at the last row should
  clamp (stay put), not wrap around or throw.
- **Typing `f` inside the new filter input itself**: must not re-trigger the focus shortcut —
  covered by the existing `e.target.tagName === 'INPUT'` guard (`web/index.html:2242`) as long
  as the `f` check is placed after that guard, same as the digit-key and `/` checks already are.
- **Subagent row expand/collapse still works**: the click-to-expand handler on `#tl-list`
  (`web/index.html:3970-3988`) must remain functional alongside the new arrow-key focus
  tracking — arrow keys move the *keyboard*-focus indicator; they should not themselves
  trigger expand/collapse (reserve Enter/Space for that if you choose to wire it, but it is
  not required by the success criteria).

### Testing Requirements

This repo has no UI test harness (per PRD §7 "Monitoring": "manual verification in-browser
per task ... this repo has no UI test harness; `make test`/`make vet` cover the Go side
only"). This task changes only `web/index.html`, so:

- Run `make test` and `go vet ./...` to confirm no Go-side regression (should be a no-op pass
  since no `.go` files change, but is a required success criterion — do not skip).
- Manually verify in-browser:
  1. Open Timeline tab, click elsewhere to blur, press `f` → filter box gains focus.
  2. From another tab (e.g. Dashboard), press `f` → Timeline tab activates and filter box
     gains focus (mirrors `/`'s cross-tab behavior).
  3. Click into the event list, press ArrowDown/ArrowUp repeatedly → focus indicator moves
     row-by-row, clamps at both ends, no console errors.
  4. Press `Escape` with session-detail panel open → still closes it.
  5. Press `/` → still jumps to Memories search, unaffected by this task's changes.
  6. Type the letter `f` inside any text input (including the new filter box) → does not
     re-trigger the shortcut or steal focus.

### Coverage Expectations

Not applicable — this is not a test task (no `*.test.*`/`*.spec.*` files affected, task type is
frontend feature work, not testing).

---

## Deliverables

None beyond the files above.

---

## Boundaries

### Files You MUST NOT Touch

From `.nimble/config.yaml` `boundaries.never_touch` (global list, applies to all tasks):

- `*.lock`
- `extension/package-lock.json`
- `.env`, `.env.*`
- `*.db`, `*.db-journal`, `*.db-wal`, `*.db-shm`
- `dist/`
- `vendor/`
- `internal/forecast/MODEL.pdf`
- `internal/forecast/archive/**`
- `bench-*.json`
- `testdata-bench-*.json`

None of these overlap with this task's scope (`web/index.html` only).

PRD-specific boundary (§11, "Files the agent should NOT touch"), also relevant:

- `internal/timeline/burn.go` and heat-view/`fs.Burn` wiring in `fleet.go` — out of scope for
  this task entirely (no Go files touched by 4a at all).
- `internal/store/sqlite.go` — no SQLite involvement in this PRD.
- `internal/parser/search.go` / `/api/sessions/search` — not used by this PRD.

### Files Requiring Review

From `.nimble/config.yaml` `boundaries.require_review`:

- `internal/auth/*`
- `internal/store/sqlite.go`
- `.goreleaser.yml`
- `.github/workflows/*`
- `extension/package.json`
- `extension/src/*`

This task does not touch any of these.

---

## Dependencies

### Upstream Tasks

| Task | What It Provides | Verify Before Starting |
|------|------------------|------------------------|
| 1a | Event-list filter `<input>` (client-side filter/search, `web/index.html`) that `f` must focus | `grep -n "tl-filter\|filterchip" web/index.html` on the current branch checkout — confirm a real filter `<input>` exists and note its exact `id` before wiring the `f` handler. As of this brief's writing, only an empty placeholder `<span id="tl-filterchip">` exists at `web/index.html:1554`; the real input does not exist yet. |
| 3a | Live/Paused toggle button for the gantt | Only required if attempting the stretch region-jump goal and choosing to include a pause-toggle shortcut. `grep -n "Live\|Paused\|flPause" web/index.html` to confirm the button's ID once 3a lands. Not required for the minimum-bar criteria (f-shortcut + arrow-key nav), which have no dependency on 3a's specific DOM. |

### Downstream Impact

None — no downstream dependencies. This is the last task in the PRD's dependency chain
(`1a → 2b → 3a → 4a`, `execution_plan.yaml`) and the sole task in Wave 4.

**Before starting:** Verify dependencies are complete by checking:
- `git log --oneline feature/timeline-feature-merger` (or the relevant task branches/PRs) for
  1a's and 3a's merge commits.
- Re-run the `grep` checks above against the live file rather than trusting any ID guessed in
  this brief — this brief was written before 1a and 3a landed, so the exact filter-box and
  pause-toggle selectors are inferred, not verified.

---

## GitHub Context

**Issue:** N/A — no GitHub issue tracker configured for this PRD (`status.json.tracker.issue_key: null`)
**Feature Issue (Parent):** N/A — same reason; tracked via NIMBLE plan item `plan_1787266927737_stpt6i`
**Branch:** `worktree/timeline-feature-merger-4a`
**Target:** `feature/timeline-feature-merger` (execution mode is `feature-branch` per `status.json`; final merge to `main` on `Olly-Oxen-Free/claumon` happens via one PR at the end of the PRD, per PRD §7)

---

## Commit Guidelines

Use Conventional Commits:
```
feat(timeline): add keyboard nav layer (f-shortcut, arrow-key event list nav)

Co-Authored-By: Claude <noreply@anthropic.com>
```

Types: feat, fix, refactor, test, docs, chore

---

## Validation Checklist

Before creating PR:
- [ ] All success criteria met
- [ ] Build passes: `make build`
- [ ] Lint passes: `go vet ./...`
- [ ] `make test` passes
- [ ] No `never_touch` files modified
- [ ] Manual browser verification steps (see Testing Requirements) completed
- [ ] Branch rebased on `feature/timeline-feature-merger`

---

*Generated by NIMBLE Brief Writer*
*PRD: timeline-feature-merger | Task: 4a | Wave: 4*
