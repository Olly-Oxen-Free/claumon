# Task Brief: 2a

**Title:** Recursive subagent hierarchy — Go parsing
**PRD:** timeline-feature-merger
**Priority:** must
**Complexity:** 4/10
**Model:** sonnet
**Wave:** 1
**Feature Issue:** TBD — `status.json.tracker.issue_key` is `null` (no GitHub issue created yet for
this PRD as of this brief). If a PM agent has since created one, use that number instead of leaving
this blank in any PR/issue you file.

---

## Objective

Fix `internal/timeline`'s subagent parsing so a subagent that itself spawned a subagent (a
grandchild agent) is correctly nested rather than lost, misattributed, or shown as an inert `Task`
row. This is the Go-side data layer for R2 (Recursive subagent hierarchy) — task 2b consumes what
this task produces to render the nesting in the gantt and event list.

---

## Context

**Parent Feature:** Timeline & Event Viewer Feature Merger (`timeline-feature-merger`, this repo's
first NIMBLE PRD; GitHub issue not yet created — see Feature Issue above).

claumon's Timeline tab shows what a session did — prompts, tool calls, and the subagents it
spawned. A subagent can itself spawn a subagent (a `Task` call made *from inside* a subagent's own
transcript, not the root session's). Today that grandchild agent is either invisible in the tree it
actually belongs to, or actively shown in the wrong place. This task is the parsing fix; task 2b
(Wave 2) makes the gantt and event list render whatever tree this task produces.

This task is part of **Wave 1** of a 4-wave plan — it runs in parallel with 1a (event-list filter)
and 1b (NIMBLE panel), touching entirely different files, so there is no file-ownership conflict
with either. Wave 2's task 2b depends on this task (`depends_on: ["1a", "2a"]`).

---

## Research Context

From `research/findings.md` (System A — claumon current state) and the PRD's §11 "Known gotchas":

> `BuildAgent` never currently calls `attachAgents` recursively — this is the literal bug to fix for
> R2 (task 2a), not a rename/refactor. `AgentRef.SpawnDepth` is already parsed and may cover part of
> the depth-tracking need. `FleetAgent` (`fleet.go:158-159`) is currently a flat struct with no
> children field — 2a must add one before 2b can render nesting.

**This brief goes further than that framing, based on reading the live code and real transcript
data on this machine (`~/.claude/projects/**/subagents/`) on 2026-08-20 — read the callout below
before starting, it changes the shape of the fix.**

### Verified against live code and live data — read this first

1. **`BuildAgent` doesn't call `attachAgents` at all — not "non-recursively," not at all.**
   `BuildAgent` (`internal/timeline/timeline.go:229-247`) calls `buildFromFile`, sets `AgentID` and
   `Project`, and returns. It never calls `attachAgents`. Compare `Build` (`timeline.go:214-226`),
   which does call it once. This means: today, when a user expands a subagent row in the event list
   (which lazily fetches `GET /api/timeline/{id}/agents/{agentId}`, i.e. `BuildAgent`), **that
   agent's own `Task` calls are never resolved into subagent events at all** — they render as plain
   `KindTool` rows named "Task," with no `Agent` ref, even when that agent's own grandchild file
   exists on disk right next to it. The PRD's "flat, one level" framing undersells this: one level
   below the session, deeper agents don't get resolved even lazily, one level at a time.

2. **All subagent files, at every spawn depth, live in one flat directory per session — not nested
   per parent.** Confirmed via `Glob`/`Grep` against real transcripts on this machine: a session
   directory's `subagents/` folder holds `agent-<id>.jsonl` + `agent-<id>.meta.json` pairs for
   *every* agent that session ever spawned, regardless of depth — there is no
   `subagents/agent-<parent>/subagents/agent-<child>.jsonl` nesting. Example, a real session with
   60+ agents (`~/.claude/projects/.../ac8e7229-.../subagents/`):

   ```
   agent-a178473671dc89ecc.meta.json:
     {"agentType":"fork","description":"Confirm PP data for 9 task orders",
      "toolUseId":"toolu_01LTiKbZ5L5uKC7Pvs66CoPq","spawnDepth":2}
   ```

   `spawnDepth: 2` — a grandchild — sitting in the exact same flat `subagents/` directory as every
   depth-1 agent. `Grep`-ing that agent's `toolUseId` across the session directory shows it appears
   **inside another subagent's own transcript**, not the root session file:

   ```
   agent-a201408273afcef29.jsonl   <- a depth-1 agent's OWN transcript contains the Task call
   agent-a201408273afcef29.meta.json: {"spawnDepth":1, ...}
   agent-a178473671dc89ecc.meta.json  <- the depth-2 (grandchild) agent's own meta, matched above
   ```

   So: **the parent-child link is not "which directory is this file in" — it's "which transcript
   (root session, or any other agent's own file) contains a `Task` `tool_use` block whose `id`
   equals this agent's `meta.toolUseId`."** `AgentRef.SpawnDepth`/`agentMeta.SpawnDepth`
   (`timeline.go:113`, `:207`) tells you *how deep*, not *whose child*. Tool-use ids are Claude's own
   globally-unique ids (`toolu_...`), so a flat cross-file id search is safe — there is no
   collision risk between sessions or agents.

3. **`attachAgents` (`timeline.go:443-557`), the function `Build` does call, has a real bug today,
   not just a limitation.** It discovers *every* agent file in `subagents/` regardless of depth
   (`timeline.go:473-501`), but only ever tries to pair by `ToolUseID` against `tl.Events`
   — the **root session's own events** (`byToolUse`/`unpairedTasks`, built at `timeline.go:454-464`
   from `tl.Events` only). A depth-2 agent's `toolUseId` will never be found there (its `Task` call
   lives inside a depth-1 agent's own file, per point 2), so it falls into `leftover`
   (`timeline.go:522-532`) and then the fallback (`timeline.go:537-553`): it either gets folded onto
   whatever *unrelated* root-level `Task` call is still unclaimed, or — if none are left — appended
   as a new **top-level** orphan event on the root timeline. Either way its cost/duration/description
   land on the wrong row today, on real sessions that already exist on this machine. This is worth
   knowing walking in: the fix is not additive ("also handle depth 2+") so much as corrective
   ("stop misattributing depth 2+ agents to the wrong row").

### What "recurse to arbitrary depth" concretely means here

Build one `toolUseID -> found agent` pool per session (all files in `subagents/`, read once — this
already happens, `timeline.go:473-501`, just needs its matching step generalized). Then fold
depth-first:

- Match `tl.Events` (root) against the pool. When a `Task` event is resolved to agent **X**, remove
  X from the pool, then immediately do the same match against **X's own `sub.Events`** (the
  timeline already built for X via `buildFromFile`, `timeline.go:481`) using the *same, now-smaller*
  pool. Recurse. A tool-use id can only be claimed once anywhere in the tree, so this terminates and
  cannot double-attach an agent or loop.
- Do this **bottom-up relative to when you compute each `AgentRef`**: fully resolve an agent's own
  children *before* building its `AgentRef` (which reads `sub.Totals.CostUSD`/`ToolCalls`,
  `timeline.go:492-496`). `finalize()` (`timeline.go:744-786`) already sums a `KindSubagent` event's
  `Agent.CostUSD`/`DurationMS`/tool-call count into its parent's `Totals` — so if `sub` has already
  had its own subagents folded in (and `finalize` re-run on it) before you read `sub.Totals` for the
  parent's `AgentRef`, cost/tool-call rollup through N levels happens for free via the existing
  logic. No separate rollup code needed — get the ordering right and this falls out.
- Leftover, never-claimed agents (missing/stale meta, no matching `toolUseId` found anywhere) keep
  the existing fallback behavior (`timeline.go:537-553`): pair with whatever `Task` calls remain
  unclaimed at the root, oldest first, then append as an orphan on the root as a last resort. Do not
  change this fallback's behavior for genuinely unpairable agents — it exists so a spawned agent is
  never silently dropped, and that guarantee should still hold.

**`BuildAgent` needs the same treatment, rooted at the requested agent instead of the session.**
Critically: the subagents *of* the requested agent live in the **same flat root-session
`subagents/` directory** as everything else — `sessionDir(path, sessionID)` (root), not a directory
scoped to `agentID`. `BuildAgent` already has `path` and `sessionID` in scope
(`timeline.go:230-239`); do not go looking for a nested `subagents/` folder under the agent's own
path, it does not exist (confirmed in point 2 above). The natural implementation is to extract the
discovery-and-fold logic `attachAgents` already has into something both `Build` and `BuildAgent` can
call, rooted at a given `*Timeline` + its own `Events`, against the one shared pool for that session.

### Recommended approach — fleet.go / FleetAgent

`agentSpans` (`fleet.go:519-560`) is deliberately cheap today: it reads only each agent file's first
timestamped line and its mtime (see the doc comment on `Fleet`, `fleet.go:19-32` — "Reading two
lines and a stat per agent keeps a week-wide view cheap, where full parsing of every agent in every
session would not be"). It reads each agent's `.meta.json` (cheap) but currently discards
`meta.ToolUseID` — only `AgentType`/`Description`/`SpawnDepth` are carried into `FleetAgent`
(`fleet.go:548-556`).

Building the true parent-child tree needs the same cross-file `toolUseId` search described above,
which means reading `Task` `tool_use` blocks out of transcript *content* — more than "two lines and
a stat." Given this file's own stated performance discipline (and `BurnSeries`'s existing pattern in
`internal/timeline/burn.go:28-32` — substring-prefilter a line before decoding it, rather than
unmarshal-everything), the recommended shape is:

- One bounded/prefiltered scan per file in the session (root + every `subagents/*.jsonl`) that
  extracts `{tool_use id -> found}` for blocks where `"name":"Task"` — filter lines by a substring
  check (e.g. contains `"type":"tool_use"` and `"name":"Task"`) before `json.Unmarshal`, same
  discipline as `BurnSeries`. This turns discovery into one pass over the session's own transcripts
  rather than a full `buildFromFile` per agent (which decodes message text, thinking blocks, and
  tool details `agentSpans` has no use for).
- Build the same depth-first fold as above, populating a `Children []FleetAgent` (or equivalent)
  field, and return the top-level (spawned directly by the session) agents from `agentSpans` as
  before, each carrying its own nested children.
- This is engineering latitude for you, not a mandated function signature — the constraint that
  matters is: don't regress `agentSpans`'s existing cheapness for a session with **no** deep
  nesting (the common case), and don't reach for a full `buildFromFile`-per-agent unless you've
  confirmed the cost is acceptable for a week-wide, many-session fleet view.

---

## Requirements

1. **`BuildAgent` resolves its own `Task` calls into nested subagent events**, using the same
   root-session `subagents/` directory and the same `toolUseId`-matching logic `Build`/`attachAgents`
   uses — not a separate/simplified scheme. A `Task` call inside the requested agent's own transcript
   that matches another agent file's `meta.toolUseId` must become a `KindSubagent` event with a
   populated `Agent *AgentRef`, exactly as it already does for the root session.
2. **`attachAgents` stops misattributing depth-2+ agents to the root.** An agent whose `toolUseId`
   is found inside another *agent's* transcript (not the root's) must be folded into that agent's
   own event list, not the root's, and not onto an unrelated root-level `Task` call.
3. **Recursion is not depth-limited.** A chain of agent → sub-agent → sub-sub-agent (3+ levels) must
   resolve correctly at every level, using the same pool-based matching regardless of depth.
4. **`FleetAgent` (`fleet.go:158-166`) gains a children field** exposing the same nested structure
   for the gantt, populated by `agentSpans` (`fleet.go:519-560`) or its caller.
5. **`fs.Burn = BurnSeries(path, start, end)` (`fleet.go:278`) and `internal/timeline/burn.go` are
   untouched.** Burn is computed once per session from the raw transcript and has nothing to do with
   agent nesting; it is an explicit PRD boundary (§11, "Files the agent should NOT touch").
6. **Existing single-level and unpairable-agent behavior is unchanged.** A session with only
   depth-1 agents, or an agent whose meta is missing/predates `toolUseId`, must produce the same
   output as today (still shown, still not dropped) — do not regress `TestAgentWithNoMetaStillAppears`,
   `TestAgentsPairWithTheirOwnTaskCallNotTheNearestOne`, or `TestSubagentCostRollsIntoTheSessionTotal`
   in `timeline_test.go`.
7. **Reuse `AgentRef.SpawnDepth`/`agentMeta.SpawnDepth`** for depth-tagging on the response shapes;
   do not re-derive depth by counting recursion levels if the meta already states it. (They should
   agree — the meta's stated depth is authoritative, since Claude Code writes it; deriving your own
   from recursion is redundant work, not an alternative source of truth.)

---

## Success Criteria

Complete ALL criteria before marking task done:

- [ ] `BuildAgent` nests subagents of subagents to arbitrary depth, verified by a 3-level test
      fixture
- [ ] `FleetAgent`/`agentSpans` expose the nested structure to callers without altering existing
      single-level sessions' output
- [ ] `BurnSeries`/`fs.Burn` wiring in `fleet.go` is untouched
- [ ] `internal/timeline` package tests cover the 3-level fixture
- [ ] `make test` and `go vet` pass

**All criteria must pass before task is complete.**

(This repo has no UI test harness; this task is Go-only and its own package tests are the primary
verification. Also manually sanity-check against a real multi-level session if one is available in
`~/.claude/projects/**/subagents/` on the machine you're working on — real spawnDepth:2+ agents do
exist on at least one developer machine as of this brief, so this is not a hypothetical shape.)

---

## Files to Modify

| File | Action | Purpose |
|------|--------|---------|
| `internal/timeline/timeline.go` | modify | Make `BuildAgent` resolve its own subagents; generalize `attachAgents`'s matching from "root events only" to a session-wide, depth-first pool match. |
| `internal/timeline/fleet.go` | modify | Add a children field to `FleetAgent`; build the nested tree in/around `agentSpans` without regressing its low-cost design for sessions with no deep nesting. |
| `internal/timeline/timeline_test.go` | modify | Add fixtures/tests for `BuildAgent` resolving nested subagents (2+ levels), and for `attachAgents`/`Build` correctly placing a depth-2 agent under its true parent rather than the root. |
| `internal/timeline/fleet_test.go` | modify | Add fixtures/tests for `FleetAgent`'s new children field across at least 3 levels of nesting. |

### File Ownership Notes

No other in-flight task touches `internal/timeline/*`. 1a and 1b are `web/index.html`-only; 3a/4a
are also `web/index.html`-only and are waved after this task. The only shared-file concern is
internal to this task itself: `timeline.go` and `fleet.go` both read `agentMeta`
(`timeline.go:203-208`, shared package-level type) — if you add any new field to `agentMeta` for
this task's own bookkeeping, both files see it; prefer not to add one unless the recursion genuinely
needs data beyond what `ToolUseID`/`SpawnDepth` already give you.

2b (Wave 2, `depends_on: ["1a", "2a"]`) consumes whatever shape you land here — see Downstream
Impact below for what it will expect.

---

## Implementation Guidance

### Patterns to Follow

- **Existing pairing primitives, generalize rather than replace.** `attachAgents`'s current
  `byToolUse`/`unpairedTasks`/`fold`/`removeIndex` machinery (`timeline.go:454-464`, `:506-520`,
  `:559-566`) is the right shape — pair-by-id, remove-on-claim, fallback-to-oldest-unclaimed,
  never-drop. Extend it to operate over a session-wide pool and recurse into each resolved agent's
  own `sub.Events`, rather than writing a parallel mechanism.
- **`readMeta` (`timeline.go:568-574`) and `agentMeta` (`timeline.go:203-208`) are already shared**
  between `timeline.go` and `fleet.go` (same package) — reuse them for the fleet-side discovery
  scan too, don't duplicate the meta-reading logic.
- **`BurnSeries`'s substring-prefilter-before-decode discipline** (`burn.go:28-32`, doc comment:
  "lines are filtered on a substring before being decoded — the difference between reading a 16MB
  file and parsing it") is the pattern to follow for any new fleet-side transcript scanning this
  task adds — this codebase treats full-file JSON unmarshal as something to avoid on the fleet path
  specifically, not on the timeline/event-list path (which already reads whole files via
  `buildFromFile`).
- **`finalize` (`timeline.go:744-786`) already rolls up subagent cost/duration/tool-calls into its
  parent's `Totals` for whatever's in `tl.Events` at the time it runs** — as covered in Research
  Context above, do the resolution bottom-up (children before the parent's own `AgentRef` is built)
  and this rollup requires no new code.

### Code Style

- Match existing doc-comment style: short paragraph explaining *why*, above the function (see every
  function in `timeline.go`/`fleet.go` for the convention — e.g. `attachAgents`'s own comment,
  `timeline.go:440-442`).
- No panics; `BuildAgent`/`attachAgents` already return/handle errors and absent files gracefully —
  keep that contract. A missing or malformed grandchild file should degrade the same way a missing
  top-level one does today (skipped, not fatal — see `buildFromFile`'s `err` handling and
  `attachAgents`'s `os.ReadDir` failure path, `timeline.go:445-448`).

### Edge Cases

- **A `toolUseId` that matches nothing anywhere in the session** (stale/predates the field, or a
  meta file that's just missing it) — keep the existing "never drop a spawned agent" fallback
  (`timeline.go:537-553`): pair with a remaining unclaimed root `Task` call, or append at the root
  as a last resort. Do not change this behavior for agents that generalize past it.
- **A `toolUseId` collision across sessions is not possible** (Claude's tool-use ids are globally
  unique) but a collision *within* one session's own pool would indicate corrupted data — not
  something to defensively code around, just don't assume you need per-session namespacing beyond
  what already exists (each session's pool is already scoped to its own `subagents/` directory).
- **`spawnDepth` in `meta.json` may be absent or `0`** on older transcripts (the field is
  comparatively new — the PRD notes it "is already parsed and may cover part of the depth-tracking
  need," not all of it). Don't require it to be present/nonzero for correct nesting — the
  `toolUseId`-based fold is what determines the tree; `SpawnDepth` is a label on top of it, not
  load-bearing for correctness. An agent with `SpawnDepth: 0` (or the field absent) whose
  `toolUseId` correctly resolves under a parent agent should still nest correctly.
- **Concurrent/parallel `Task` calls within one turn** (multiple `tool_use` blocks in one assistant
  message) already work today for direct root pairing (see
  `TestParallelToolsCloseAgainstTheirOwnResults`, `timeline_test.go:137-163`, for the general
  parallel-tool pattern this codebase already handles) — the same should hold when those parallel
  `Task` calls are made from *inside* a subagent's own transcript, not just the root's.
- **`BuildAgent`'s existing traversal guard (`safeAgentID`, `timeline.go:250-260`)** only validates
  the *requested* agent's id from the URL. When you resolve that agent's own children, you already
  trust `agentID` values coming from `os.ReadDir` on the local `subagents/` directory (as
  `attachAgents` already does, `timeline.go:445-478`) — you do not need to re-run `safeAgentID`
  against ids discovered from disk, only against ids coming from outside (the URL path value). Don't
  add redundant validation that changes behavior for legitimately-discovered agent ids.

### Testing Requirements

Extend `internal/timeline/timeline_test.go` and `internal/timeline/fleet_test.go` using the existing
fixture helpers (`fixture`, `writeAgent`, `agentWork`, `fleetFixture` — do not invent a parallel
fixture mechanism). The existing `writeAgent` helper already lets you write an arbitrary agent
transcript body (`[]map[string]any`), so a nested fixture is: give a depth-1 agent's own written
`lines` a `toolUse(...)` block (a `Task` call) with some id, then `writeAgent` a second agent whose
`agentMeta.ToolUseID` matches that id — both files sit in the same flat `subagents/` directory the
existing helper already writes to (`timeline_test.go:256-275`), matching the real on-disk layout
confirmed in Research Context above. No new directory-layout fixture helper should be needed; extend
`agentWork`/write custom line sequences as needed for the parent level that itself makes a `Task`
call.

Minimum coverage:

1. **`timeline_test.go`:** A 3-level fixture (session → agent A → agent B → agent C). Assert, via
   `Build`, that `A` is a `KindSubagent` on the root, `A.Agent` is populated, and (however you expose
   it) `B` is reachable as `A`'s child rather than appearing as a root-level or misattributed event.
   Assert, via `BuildAgent(dir, id, "A")`, that `A`'s own returned `Timeline.Events` contains `B` as
   a resolved `KindSubagent` (not an inert `Task` `KindTool` row) — this is the specific case that
   is completely broken today (Research Context, point 1).
2. **`timeline_test.go`:** A regression case proving a depth-2 agent no longer lands on the wrong
   root-level `Task` call or gets appended as a root orphan when its true parent (a depth-1 agent)
   is present in the fixture.
3. **`fleet_test.go`:** A 3-level fixture through `BuildFleet`, asserting the new children field on
   `FleetAgent` nests correctly to depth 3, and that `TestFleetReportsAgentsAsSpans`'s existing
   single-level case (`fleet_test.go:87-126`) still passes unchanged.
4. Run existing tests to confirm no regressions, in particular `TestSubagentIsFoldedIntoTheTaskThat
   SpawnedIt`, `TestAgentsPairWithTheirOwnTaskCallNotTheNearestOne`, `TestAgentWithNoMetaStillAppears`,
   `TestSubagentCostRollsIntoTheSessionTotal`, `TestBuildAgentReturnsTheAgentsOwnEvents` (all in
   `timeline_test.go`), and `TestFleetReportsAgentsAsSpans` (`fleet_test.go`).

Coverage Expectations section omitted — task/file-naming detection criteria (title containing
"test," task type "testing," or `files_affected` matching `*.test.*`/`*.spec.*`/`__tests__/*`) don't
match this task: it's a parsing/feature fix whose `files_affected` happens to include Go's
`_test.go` files as part of normal test-alongside-code practice, not a dedicated test task.

---

## Deliverables

None beyond the files above (inferred — `tasks.yaml` task 2a has no `deliverables` field; this was
not captured at interview, so this line is inferred, not verified against a human-confirmed artifact
list).

---

## Boundaries

### Files You MUST NOT Touch

From `.nimble/config.yaml` global `never_touch` (applies repo-wide): `*.lock`,
`extension/package-lock.json`, `.env`, `.env.*`, `*.db`, `*.db-journal`, `*.db-wal`, `*.db-shm`,
`dist/`, `vendor/`, `internal/forecast/MODEL.pdf`, `internal/forecast/archive/**`, `bench-*.json`,
`testdata-bench-*.json`. None of these are anywhere near this task's scope.

**Task-specific (from `tasks.yaml`, the PRD §6/§11, and this task's own description):**

- **`internal/timeline/burn.go`** — do not touch. Recently stabilized (PRD §11 cites commits
  `af8dac2`, `f7cd4b6`); unrelated to agent nesting.
- **`fs.Burn = BurnSeries(path, start, end)` at `fleet.go:278`, and the surrounding `if withBurn {`
  block (`fleet.go:277-279`)** — do not modify this call site even incidentally while editing
  nearby code in `BuildFleet`. If your `FleetAgent`-tree-building code needs to sit near this
  function, add it as its own clearly separate block/call, not interleaved with the burn branch.
- **`web/index.html`** — this is a Go-only task (task 2b, Wave 2, handles all frontend rendering of
  whatever this task produces). Do not touch it, even to sanity-check rendering — verify via Go
  tests and, if useful, `curl`/manual inspection of the JSON API response shape instead.
- **`internal/parser/search.go`, `internal/server/handlers.go`** — out of scope for this task (R1's
  boundary, not R2's, but listed for completeness — this task has no reason to touch either; if you
  find yourself needing a new server route, stop and reconsider, none is called for here since
  `BuildAgent`/`BuildFleet` are called from existing, unchanged handlers).

### Files Requiring Review

From `.nimble/config.yaml`: `internal/auth/*`, `internal/store/sqlite.go`, `.goreleaser.yml`,
`.github/workflows/*`, `extension/package.json`, `extension/src/*`. This task does not touch any of
these — no review-gate concern for 2a.

---

## Dependencies

### Upstream Tasks

None — `depends_on: []` in `tasks.yaml`. This task can start immediately.

### Downstream Impact

**2b** (Recursive subagent hierarchy — gantt and event-list rendering, `depends_on: ["1a", "2a"]`)
consumes this task's output directly:

- It will call `GET /api/timeline/{id}` (`Build`) and expect the returned `Timeline.Events` to
  contain correctly-nested `KindSubagent` events for however deep this task resolves them.
- It will call `GET /api/timeline/{id}/agents/{agentId}` (`BuildAgent`) repeatedly, one level per
  user expand-click (the existing "lazy-fetch-per-expand pattern" the PRD's UX notes describe,
  §5) — each call must return that agent's own `Task` calls already resolved into `KindSubagent`
  events, exactly as `Build` does for the root. If `BuildAgent` still returns inert `Task`
  `KindTool` rows for an agent that has its own children, 2b's expand-to-recurse UX has nothing to
  expand into.
- It will read whatever children field you add to `FleetAgent` (`fleet.go:158-166`) to indent
  gantt rows per depth — name and shape this field clearly (a plain `[]FleetAgent`/similarly-named
  slice is the natural choice; there's no existing convention elsewhere in this package to match
  beyond that).

**Before starting:** No upstream dependency to verify — this is a Wave 1 task with no `depends_on`.

---

## GitHub Context

**Issue:** TBD — no issue created yet for this task (see Feature Issue note above).
**Feature Issue (Parent):** TBD — `status.json.tracker.issue_key` is `null`.
**Branch:** `worktree/timeline-feature-merger-2a`
**Target:** `feature/timeline-feature-merger` (per `status.json`: `execution_mode:
"feature-branch"`, `feature_branch: "feature/timeline-feature-merger"` — PRs land on this branch,
not `main`, per the PRD's Rollout Plan).

---

## Commit Guidelines

Use Conventional Commits:
```
fix(timeline): resolve subagent hierarchy to arbitrary depth

Co-Authored-By: Claude <noreply@anthropic.com>
```

Types: feat, fix, refactor, test, docs, chore

(`fix` is suggested over `feat` since this is fundamentally a correctness fix — `BuildAgent`
silently failing to resolve its own subagents, and `attachAgents` misattributing depth-2+ agents to
the wrong row — rather than new surface area; use your judgment if you land it differently.)

---

## Validation Checklist

Before creating PR:
- [ ] All success criteria met
- [ ] Build passes: `make build`
- [ ] Type check: N/A (`.nimble/config.yaml` has `typecheck: null` for this project — Go project,
      no separate typecheck step; `go vet ./...` and `make build` are the relevant static checks)
- [ ] `go vet ./...` passes
- [ ] `make test` passes, including new 3-level nesting fixtures in both `timeline_test.go` and
      `fleet_test.go`
- [ ] No `never_touch` files modified
- [ ] `internal/timeline/burn.go` untouched; `fs.Burn = BurnSeries(...)` call site at `fleet.go:278`
      untouched
- [ ] `web/index.html` untouched (this task is Go-only)
- [ ] Existing single-level and unpairable-agent tests still pass unchanged (see Testing
      Requirements above for the specific test names to check)
- [ ] Branch rebased on `feature/timeline-feature-merger`

---

*Generated by NIMBLE Brief Writer*
*PRD: timeline-feature-merger | Task: 2a | Wave: 1*
