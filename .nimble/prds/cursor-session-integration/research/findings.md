# Research: cursor-session-integration

Bounded research pass on 2026-08-20, done to seed a future `/nimble:plan` for detecting and
displaying Cursor sessions in claumon's Timeline/Dashboard, requested after using the merged
timeline-feature-merger work. Not exhaustive — enough to scope, not to implement.

## What claumon has today

Zero Cursor support. No parser code, no format handling, no detection anywhere in `internal/`
or `web/index.html` (confirmed via grep). Everything claumon reads today assumes Claude Code's
own `~/.claude/projects/**/*.jsonl` transcript format.

## Where Cursor actually stores session data (this machine)

Two separate locations, both real and populated — `~/.cursor/` is mostly *not* where the data
lives:

- `~/.cursor/ai-tracking/ai-code-tracking.db` — SQLite. Tables: `ai_code_hashes`,
  `conversation_summaries`, `scored_commits`, `tracked_file_content`, `ai_deleted_files`,
  `tracking_state`. **Empty on this machine** (0 rows in `conversation_summaries`) — this looks
  like a code-attribution ledger (which lines came from AI vs human), not the primary session
  store, and isn't populated by normal use.
- `~/.config/Cursor/User/globalStorage/state.vscdb` — the real one. Cursor is VSCode-based;
  this is VSCode's own SQLite state store, repurposed. Three tables:
  - `ItemTable` (key/value, JSON-ish) — has `aiCodeTracking.dailyStats.v1.5.*` keys and other
    app settings, not conversation content itself.
  - `composerHeaders` (`composerId TEXT PRIMARY KEY, workspaceId, createdAt, lastUpdatedAt,
    isArchived, isSubagent, recency, checkpointAt, value TEXT`) — **819 real rows on this
    machine**. `value` is a JSON blob per conversation: `composerId`, `createdAt`/
    `lastUpdatedAt`, `unifiedMode` ("agent"/"edit"/etc), `subtitle` (effectively the session's
    title — first-message-derived, same role as claumon's session title today),
    `draftTarget.environment.uri.fsPath` (the workspace/project path — equivalent to claumon's
    `cwd`), `totalLinesAdded`/`totalLinesRemoved`, `isWorktree`. This is the closest analog to
    claumon's `SessionSummary`.
  - `cursorDiskKV` (`key TEXT UNIQUE, value BLOB`) — **301,920 rows on this machine**. Two key
    patterns observed: `agentKv:blob:{hash}` (content-addressed, purpose not yet determined —
    likely large attachments/diffs, not investigated further) and
    `bubbleId:{composerId}:{bubbleId}` — **one row per message** in a conversation. This is the
    actual transcript.
- Each workspace *also* has its own `state.vscdb` under
  `~/.config/Cursor/User/workspaceStorage/{workspaceHash}/` — not inspected; unknown whether it
  duplicates or supplements the global store. Open question for the real planning pass.

## Bubble (message) schema — richer than expected

One `bubbleId:*` row's JSON value has ~70 keys. Notable ones relative to claumon's existing
`timeline.Event` model: `type` (int — role/kind discriminator, not yet mapped to
user/assistant/tool), `text`, `toolFormerData` (tool call data — the `tool_use`/`tool_result`
analog), `codeBlocks`, `allThinkingBlocks`, `images`, `attachedCodeChunks`,
`suggestedCodeBlocks`, `gitDiffs`, `tokenCount`, `requestId`, `createdAt`.

**`allThinkingBlocks` is present and worth flagging directly against this session's earlier bug
report (items 5/6, "show the actual thinking transcript" / "describe thinking level"):**
Claude Code's own transcripts never persist reasoning text — confirmed in
`internal/timeline/timeline.go`'s own comments ("the reasoning itself is not recoverable").
Cursor's bubble schema *has* a dedicated field for it. Whether it's actually populated with
real text (vs. also stripped) wasn't checked in this pass — first thing to verify before
promising this as a Cursor-only capability claumon could surface that Claude Code sessions
never can.

## Scope signal

This is a second full ingestion pipeline, not a tweak: new SQLite reads (`cursorDiskKV`
message-shape reverse-engineering, `type` int → role/kind mapping, tool-call shape mapping to
claumon's existing `Kind`/`Title`/`Detail` model), a session-summary adapter parallel to
`internal/parser.SessionSummary`, provider detection (Claude Code vs Cursor, and which of them
authored a given session if the API is used from either), and the requested UI surface
(highlight, hyperlink, icon on Cursor-sourced rows in claumon's existing session/timeline
views). Comparable in size to what `agents-observe` did with its separate Claude/Codex
transcript parsers (see `.nimble/prds/001_timeline-feature-merger/research/findings.md`,
System B) — a second `internal/cursor` (or similar) package mirroring `internal/parser`'s
role, not an extension of the existing Claude Code parser.

## Open questions for the real planning interview

- Does `bubbleId`'s `type` field reliably map to user/assistant/tool-call/tool-result, and is
  there a stable enum, or does it need reverse-engineering per Cursor version?
- Is `allThinkingBlocks` actually populated with real reasoning text on real usage, or also
  stripped like Claude Code's?
- Per-workspace `state.vscdb` under `workspaceStorage/` — duplicate of global, or the only
  place some data lives (e.g. workspace-scoped conversations)?
- `agentKv:blob:*` keys in `cursorDiskKV` — what are they, and are they needed for full
  transcript fidelity or safely ignorable?
- Live-update story: claumon's SSE/file-watcher model assumes JSONL files (fsnotify on file
  writes). SQLite (`state.vscdb`, WAL mode) needs a different watch strategy — poll, or watch
  the `-wal` file for writes.
- Does the user want read-only detection/display (matches this PRD's title ask: "highlight +
  hyperlink + icon"), or eventually the same depth of timeline/event-viewer detail claumon
  gives Claude Code sessions?
