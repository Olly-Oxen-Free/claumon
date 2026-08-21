```yaml
---
feature_name: "Cursor Session Integration"
feature_slug: "cursor-session-integration"
owner: "jayden-eppcohen"
status: "draft"
created_date: "2026-08-20"
target_date: ""
phase: ""
scope_type: "integration"
github_project: ""
links: []
checkpoint_refs: []
cross_feature_blockers: []
---
```

---

## 1. Executive Summary

**One-liner:** Detect and surface Cursor editor sessions in claumon (highlight, hyperlink,
icon) alongside Claude Code sessions, and auto-detect when Cursor is itself driven via the
Claude API (in which case it should already show up as a Claude Code-style session, not need
separate detection).

**What's changing:** claumon currently only reads `~/.claude/projects/**/*.jsonl`. It has zero
awareness that Cursor sessions exist. After this ships, Cursor-sourced sessions appear in
claumon's session list/dashboard/timeline, visually distinguished the same way Nimbalyst- and
herdr-hosted sessions already are (icon + hyperlink, per the existing `.fl-nimbic`/`.fl-herdric`
pattern in `web/index.html`).

**Why now:** Raised as a follow-up request while reviewing the merged timeline-feature-merger
work — the user wants Cursor sessions visible the same way Claude Code sessions already are.

**Status of this document:** **Draft, seeded by a bounded research pass, not a full interview.**
This PRD has no `tasks.yaml`/`execution_plan.yaml` yet and is not ready for `/nimble:run`. Run
`/nimble:plan --prd cursor-session-integration` for the real interview (framing, requirements,
dependencies, orchestration config) before executing. See `research/findings.md` in this folder
for what's already been checked.

**Primary risk:** Cursor's session storage is undocumented, version-dependent SQLite internals
(`~/.config/Cursor/User/globalStorage/state.vscdb`), not a stable public format the way Claude
Code's JSONL transcripts effectively are. Schema drift across Cursor versions is a real
maintenance risk this PRD's interview should size explicitly.

---

## 2. Problem & Context

**Problem statement:** The user works across both Claude Code and Cursor. claumon, a
"real-time Claude Code dashboard," is blind to any work done in Cursor — it doesn't exist in
the tool's model at all.

**Supporting data:** See `research/findings.md` — confirms real, populated Cursor session data
exists on this machine (819 conversations in `composerHeaders`, 301,920 message-level rows in
`cursorDiskKV`), so this is buildable, not speculative.

**Strategic fit:** claumon already special-cases *where* a Claude Code session runs (Nimbalyst,
herdr) via icon+hyperlink. This extends the same visual pattern to *which tool* ran the
session, for a second tool entirely — a bigger scope jump than the existing precedent, since it
requires parsing a second, undocumented data source rather than just tagging metadata onto an
already-parsed Claude Code session.

---

## 3. Goals, Non-Goals & Success Metrics

### Goals

1. Detect real Cursor sessions on disk and list them alongside Claude Code sessions.
2. Visually distinguish them: icon + highlight + hyperlink (opens Cursor to that session, if
   Cursor exposes a reveal/deep-link mechanism — unconfirmed, check during interview).
3. Auto-detect when a session attributed to "Cursor" is actually running against the Claude
   API (per the user's framing: "obviously Cursor isn't Claude unless we use our api, which it
   should also be able to detect as being cursor") — i.e., distinguish Cursor-as-editor-shell
   using Claude models from Cursor's own models, and label accordingly rather than conflating.

### Non-Goals (proposed — confirm in interview)

- Full timeline/event-viewer depth for Cursor sessions (recursive hierarchy, filter/search,
  live gantt) matching what Claude Code sessions get in this PRD's first pass — likely a
  follow-up, not bundled here.
- Reverse-engineering every field in Cursor's bubble schema — only what's needed for
  session-list-level display (title, workspace, timestamps, model) plus whatever's needed for
  Goal 3's model detection.

### Success Metrics

| Metric | Baseline | Target | How Measured |
| ------ | -------- | ------ | ------------- |
| Cursor sessions visible | 0 | All real Cursor conversations from `composerHeaders` appear in claumon's session list | Manual check against this machine's 819 real conversations |
| Visual distinction | N/A | Icon + hyperlink on every Cursor-sourced row, matching the Nimbalyst/herdr pattern | Manual check |
| Claude-API-via-Cursor detection | N/A | A session run through Cursor using a Claude model is labeled distinctly from one using a non-Claude model | Manual check against real mixed-model conversation history |

---

## 4. Requirements

Not yet decomposed — pending the real interview. Candidate must-haves based on the user's ask
and research findings:

| ID | Requirement (candidate) | Notes |
| -- | ------------------------ | ----- |
| R1 | Parse `composerHeaders` + resolve `bubbleId:*` messages from `state.vscdb` into a session-summary shape parallel to `internal/parser.SessionSummary` | New `internal/cursor` (or similar) package, mirroring the `internal/parser`/`internal/timeline` split |
| R2 | Icon + hyperlink + highlight on Cursor-sourced rows in the session list/dashboard | Follows the existing `.fl-nimbic`/`.fl-herdric` CSS/JS pattern |
| R3 | Detect Claude-API-backed Cursor sessions vs. Cursor's own models, label distinctly | Needs the `type`/model fields in bubble JSON mapped and confirmed first |

---

## 5. UX & Interaction Notes

Not yet decided — interview needed. Candidate: reuse the existing session-list row pattern
rather than inventing a new one, consistent with how Nimbalyst/herdr integrations were added
without a new UI paradigm.

---

## 6. Dependencies & Risks

### Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
| ---- | ---------- | ------ | ---------- |
| Cursor's SQLite schema is undocumented and may change across Cursor versions | High | Medium | Scope R1 defensively (tolerate missing/unexpected fields, per the same defensive-parsing pattern used for NIMBLE's own under-documented `status.json` in timeline-feature-merger); pin against the Cursor version(s) actually in use on this machine first |
| `bubbleId`'s `type` int → role/kind mapping isn't confirmed | Medium | Medium | Resolve during interview/research phase before task decomposition, not during implementation |
| Live-update: SQLite (WAL) doesn't fit claumon's fsnotify-on-JSONL-writes model | Medium | Low | Likely needs polling or `-wal` file watching; decide cadence during interview |
| Per-workspace `state.vscdb` under `workspaceStorage/` may hold data the global store doesn't | Low | Medium | Check before committing to "global store only" as the read source |

---

## 7. Rollout Plan

Not yet decided — pending interview. Same fork (`Olly-Oxen-Free/claumon`), same feature-branch
NIMBLE workflow as timeline-feature-merger, presumably.

---

## Research Findings

See `research/findings.md` in this PRD folder. Summary: real, populated Cursor session data
confirmed on this machine (`~/.config/Cursor/User/globalStorage/state.vscdb`:
`composerHeaders` = 819 conversations, `cursorDiskKV` = 301,920 message-level rows keyed
`bubbleId:{composerId}:{bubbleId}`). Notably, Cursor's bubble schema has an `allThinkingBlocks`
field — unlike Claude Code, which never persists reasoning text at all (confirmed in claumon's
own `internal/timeline/timeline.go` comments). Whether it's actually populated wasn't checked;
flagged as the first thing to verify, since it would let claumon show real reasoning for Cursor
sessions in a way it fundamentally cannot for Claude Code ones.

---

## Agent Tasks

Not yet generated — this PRD has no `tasks.yaml`. Run `/nimble:plan --prd
cursor-session-integration` to conduct the interview and produce one.

---

*Draft PRD seeded by research, 2026-08-20. Not reviewed, not approved, not executable.*
