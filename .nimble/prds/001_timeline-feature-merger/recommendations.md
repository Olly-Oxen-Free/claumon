# Pre-Execution Review — timeline-feature-merger

**Reviewed:** briefs 1a, 1b, 2a, 2b, 3a, 4a against live `feature/timeline-feature-merger`
codebase state, `PRD_timeline-feature-merger.md`, `tasks.yaml`, `execution_plan.yaml`,
`status.json`.

**Method:** every file:line citation in all six briefs was checked against the live checkout
with `grep`/`sed`/`Read`; Go build/vet/test baseline was confirmed clean; two other on-disk
`.nimble/` installations (`nimbalyst_worktrees/*`) were used to check the `status.json` schema
claim in brief 1b.

**Overall assessment:** this brief set is unusually well-grounded — essentially every
file:line/function-name citation across all six briefs checked out exactly against the live
branch (no waves have landed yet, so citations are still fresh). No Critical findings that would
cause an execution failure. Two Warnings worth acting on before execution, plus several
Observations.

---

## Critical Findings

None. No brief asserts a codebase state that is actually false, no success criterion is
infeasible, no file path is wrong, and no wave-ordering / dependency mismatch was found between
`tasks.yaml`, `execution_plan.yaml`, and the six briefs.

---

## Warnings

### Warning 1 — 1b's brief flags the `status.json` schema as unverifiable, but it is verifiable right now on this machine

**Affected File:** `briefs/1b_timeline-feature-merger.md:49, 62` ("Known Issues to Address" /
"BLOCKED/STALE/CRASHED surfacing")

**Actual state:** Two other populated `.nimble/prds/*/status.json` files exist on this machine
and were not consulted when the brief was written:

- `/home/jayden-eppcohen/Documents/Projects/Software/nimbalyst_worktrees/Agent-visibility-and-Workflow-Enhancement/.nimble/prds/001_agent-visibility/status.json`
- `/home/jayden-eppcohen/Documents/Projects/Software/nimbalyst_worktrees/project-bar-movement/.nimble/prds/001_project-bar-top-placement/status.json`

The first is a populated example (not `"tasks": {}`):

```json
"waves": {
  "1": { "status": "pending" }, "2": { "status": "pending" }, ...
},
"tasks": {
  "1a": { "status": "queued", "wave": 1, "model": "opus" },
  "1b": { "status": "queued", "wave": 1, "model": "opus" },
  ...
}
```

This confirms `tasks` is a flat map keyed by task id, each value `{status, wave, model}` — which
the brief's "defensive parsing, don't assume a shape" framing already handles safely. But it also
reveals a field the brief's data model **never mentions at all**: a top-level `status.json.waves`
object, keyed by wave number, each with its own `status` field ("pending" here). This is a more
direct, authoritative source for "wave progress" than deriving it by cross-referencing
`execution_plan.yaml`'s wave→task-id map against per-task `status.json.tasks[id].status` — which
is the only mechanism 1b's brief describes (Requirements §1, Recommended Approach in Research
Context).

**Problem:** Not a correctness blocker — deriving wave progress from task states would still
work, and the brief's defensive-parsing instruction ("treat unknown/missing status fields as
unknown rather than crashing") already protects against an unrecognized shape. But the executing
agent will spend implementation effort re-deriving what `status.json.waves` already states
directly, and might miss displaying a wave's own status string (e.g. a future `"in_progress"` or
`"blocked"` wave-level value) since the brief's data model doesn't ask for it.

**Correction Needed:** Update brief 1b's Research Context / Requirements to mention
`status.json.waves` (map of wave-number → `{status}`) as a field to read and surface directly,
alongside the per-task `status.json.tasks[id].status` map already covered. Point the correction
agent at the two files above as live, on-disk schema examples (both are outside this repo but on
the same machine — readable, not synthetic).

---

### Warning 2 — 2a's true severity (live data misattribution) is undersold in `tasks.yaml`/PRD, though not in 2a's own brief

**Affected File:** `tasks.yaml:66-76` (task 2a `description`), `PRD_timeline-feature-merger.md:300-302`
("Known gotchas discovered since research")

**Actual state:** Verified directly against `internal/timeline/timeline.go`:
- `Build` (line 214-226) calls `attachAgents` once; `BuildAgent` (line 229-244) never calls it at
  all — confirmed, matches brief 2a's claim #1.
- `attachAgents` (line 443-557) builds `byToolUse` **only from `tl.Events`, the root session's own
  events** (line 454-464), then discovers every `.jsonl` file in the session's flat `subagents/`
  directory regardless of depth (line 473-501) and tries to pair each by `ToolUseID` against that
  root-only map. A depth-2 agent's `toolUseId` lives inside a depth-1 agent's own transcript, not
  the root's, so it is never found (`ok` is false at line 524-525), falls into `leftover`, and then
  gets folded onto an unrelated unclaimed root `Task` call or appended as a root-level orphan
  (line 537-553) — confirmed, matches brief 2a's claim #3 exactly: this **actively misattributes**
  cost/duration/description onto the wrong row on **every currently-shipped session with
  depth-2+ agents**, right now, in production `Build()` — not a missing-feature gap, an active
  correctness bug in code that already ships.

`tasks.yaml`'s task 2a description and the PRD's §11 "Known gotchas" both frame this only as
"`BuildAgent` never currently calls `attachAgents` recursively... this is the literal bug to fix,"
which describes the `BuildAgent` half accurately but omits the `attachAgents`/`Build` misattribution
half entirely (brief 2a's own "Verified against live code and live data" section, written after
research, is what surfaces this — it explicitly says "the PRD's 'flat, one level' framing
undersells this").

**Problem:** Not an execution blocker — brief 2a itself is accurate, thorough, and its own
success criteria/regression-test list already cover this correctly (it names the exact existing
tests that must not regress). But `tasks.yaml` and the PRD are the artifacts most likely to be
read by anyone auditing priority/risk after the fact, and they still describe 2a as filling a gap
rather than fixing active data corruption on real sessions. This affects how urgently 2a should be
treated relative to its Wave-1 siblings (1a: isolated new frontend UI, 1b: isolated new package/
tab) — 2a's blast radius if its own fix regresses is every session with nested agents, not just a
missing-feature scenario, and its correctness matters independent of whether 2b (Wave 2) ever
lands.

**Correction Needed:** Update `tasks.yaml`'s task 2a `description` and/or PRD §11 "Known gotchas"
to state plainly that `attachAgents` (called via the already-shipped `Build`) currently
misattributes depth-2+ agents' cost/duration/description to the wrong event, not just that nesting
is unsupported. Consider whether 2a's `Model: sonnet` assignment (vs. 1b's `opus` at complexity 5)
still matches the actual risk profile now that this is understood — 2a's brief is the longest and
most intricate of the six despite its stated complexity of 4/10.

---

## Observations

### Observation 1 — Naming-collision warnings in the briefs are all real, not speculative

Checked directly against `web/index.html`:
- `#tl-filterchip` (line 1554), `.tl-chip`/`.tl-chip-name`/`.tl-chip-x` CSS (lines 722-732), and
  `tlRenderFilterChip()` (line 4224) all exist exactly as brief 1a describes, and are indeed the
  unrelated gantt session-focus chip. 1a's own new element IDs (`tl-filter-text`, `tl-filter-tool`,
  `tl-filter-errors`) do not collide with any of these. The collision risk 1a calls out is real,
  and 1a's own plan avoids it.
- `.fl-bar.live` (line 669) and `.tl-live` (line 827) both exist exactly as brief 3a warns; 3a's
  chosen `fl-live-btn`/`data-live-toggle` naming avoids both. The collision risk is real and 3a's
  plan avoids it.
- `flRender()` does rebuild `#tl-graph`'s entire `innerHTML` including a fresh `.fl-scroll` element
  on every call (line 4727), and the existing `flRefresh()` (lines 4392-4396) already captures
  `scroller.scrollLeft` into `flPendingScrollLeft` immediately before triggering a reload — 3a's
  brief mirrors this exact existing pattern in its own click handler and calls out the scroll-reset
  risk explicitly in its Edge Cases section. This is correctly specified, not hand-waved.

### Observation 2 — 2b's "event list may already work with little/no JS change" claim holds up

Traced the actual pipeline: `tlEventRow` (line 3829) is called recursively with the same
closure-captured top-level `sessionId` at every nesting level (line ~4016,
`(sub.events || []).map(e => tlEventRow(e, sessionId))`), the subagent-expand click handler uses
event delegation via `.closest('.tl-row.subagent')` (line ~3987) so it is depth-agnostic, and
`BuildAgent`'s target agent's own children live in the same flat root-session `subagents/`
directory regardless of depth (confirmed in `internal/timeline/timeline.go`). Once 2a's fix makes
`BuildAgent` resolve its own `Task` calls the same way `Build` does, this existing JS chain should
indeed require no structural change — 2b's claim is well-supported by code tracing, not hand-waving.
The one real gap 2b's brief itself flags (`flOpenAgent` deep-linking past depth 1,
`web/index.html:5008-5017`) is correctly scoped as a known, accepted limitation rather than a
success-criterion.

### Observation 3 — 2a/2b agree on data shape by design, not by coincidence

2a's brief deliberately leaves the exact `FleetAgent` children field name/shape as "engineering
latitude," and 2b's brief explicitly defers to re-reading `internal/timeline/fleet.go` live once
2a merges rather than assuming a JSON key. Since 2b is a pure JSON consumer of whatever 2a lands
(no shared Go types edited by 2b), there is no coordination gap here — this is a sound way to
decouple two tasks that touch different files without over-specifying an interface upfront.

### Observation 4 — 1b's `fetchNimbalystSessions` citation is imprecise but the underlying warning is correct

Brief 1b (Code Style section) warns to "grep for `nimbalyst` before naming anything" and cites
`fetchNimbalystSessions` as an example existing identifier. That exact function name does not
exist in `web/index.html` (verified via grep — no match). The underlying collision risk is real,
though: `/api/nimbalyst/sessions`/`/api/nimbalyst/reveal` routes, `s.nimbalyst`, `inNimbalyst`, and
`.fl-name.nimbalyst` CSS all exist and are unrelated to the new "NIMBLE" panel. The corrective
instruction (grep for `nimbalyst` before naming) is sound and sufficient regardless of the
fabricated example name — no action needed, this is FYI only.

### Observation 5 — Baseline is clean

`go build ./...` and `go vet ./...` both pass cleanly on `feature/timeline-feature-merger` before
any task starts, and every test name cited by brief 2a as a required non-regression
(`TestAgentWithNoMetaStillAppears`, `TestAgentsPairWithTheirOwnTaskCallNotTheNearestOne`,
`TestSubagentCostRollsIntoTheSessionTotal`, `TestBuildAgentReturnsTheAgentsOwnEvents`,
`TestSubagentIsFoldedIntoTheTaskThatSpawnedIt`, `TestParallelToolsCloseAgainstTheirOwnResults`,
`TestFleetReportsAgentsAsSpans`) exists exactly as named in `internal/timeline/timeline_test.go` /
`fleet_test.go`, as do the fixture helpers (`fixture`, `writeAgent`, `agentWork`, `fleetFixture`)
2a's brief tells the implementer to reuse.

---

## Correction Summary

| # | Finding | Action | File to Correct |
|---|---------|--------|------------------|
| 1 | `status.json.waves` field exists and is unmentioned | Add `status.json.waves` (map of wave-number → `{status}`) to 1b's Research Context/Requirements as a direct wave-progress source; cite the two on-disk example files found on this machine | `briefs/1b_timeline-feature-merger.md` |
| 2 | `tasks.yaml`/PRD undersell 2a's severity | Rephrase 2a's `tasks.yaml` description and/or PRD §11 to state the `attachAgents` misattribution bug plainly (active data corruption on real sessions today, not just a missing feature); reconsider model tier | `tasks.yaml`, `PRD_timeline-feature-merger.md` |

Both are documentation/framing corrections, not correctness fixes to the briefs' actual
instructions — the six briefs are internally accurate and executable as written.

---

## Execution Clearance

**CLEARED** — no Critical findings. The two Warnings are recommended but non-blocking:
Warning 1 would make 1b's implementation slightly more direct/correct; Warning 2 is a
documentation-accuracy issue in `tasks.yaml`/PRD that doesn't affect what the executing agent for
2a will actually do (2a's own brief already has the correct, thorough picture and appropriate
regression-test coverage).
