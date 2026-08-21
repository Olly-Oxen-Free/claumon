// Package timeline turns a session transcript into an ordered stream of what
// the agent actually did: prompts, replies, tool calls with how long each took,
// and the subagents it spawned.
//
// claumon's session parser answers "what did this session cost". It merges
// tool results into the assistant message that requested them, which is right
// for a cost summary and wrong for a timeline: the merge discards the result's
// timestamp, and that difference is a tool call's duration.
//
// So this reads the transcript itself. Costs still route through
// parser.MessageCostUSD so there is one pricing implementation.
//
// Subagents live in their own files: a session's transcript sits at
// <project>/<session>.jsonl, and each agent it spawned at
// <project>/<session>/subagents/agent-<id>.jsonl with a sibling
// agent-<id>.meta.json naming the agent type, its task, and — the link that
// makes nesting possible — the `toolUseId` of the parent's Task call.
package timeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fabioconcina/claumon/internal/parser"
)

// Kind classifies a timeline entry.
type Kind string

const (
	// KindPrompt is a message from the user.
	KindPrompt Kind = "prompt"
	// KindMessage is assistant prose (no tool call attached).
	KindMessage Kind = "message"
	// KindTool is one tool invocation and its result.
	KindTool Kind = "tool"
	// KindSubagent is a Task call, joined to the agent it spawned.
	KindSubagent Kind = "subagent"
	// KindThinking is a model response that reasoned before acting.
	//
	// The reasoning itself is not recoverable: Claude Code writes thinking
	// blocks with an empty body and a signature only, so the text shown live in
	// the terminal is never persisted. What this event gives is the structure —
	// a parent for the tool calls that came out of that turn, which otherwise
	// sit in the list with nothing above them.
	KindThinking Kind = "thinking"
	// KindCompact is a context compaction: the point where the conversation
	// was summarised and most of its history dropped. Worth marking, because
	// everything before it is no longer in the model's context, and a session
	// often behaves differently on either side of one.
	KindCompact Kind = "compact"
)

// Event is one entry in the timeline.
type Event struct {
	Seq  int       `json:"seq"`
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`
	// DurationMS is how long a tool call or subagent ran. Zero when unknown,
	// which is the honest answer for a call whose result never arrived.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Title is the tool name, the agent type, or the role.
	Title string `json:"title"`
	// Detail is a one-line summary: the tool's key argument, the subagent's
	// task, or the start of the message text.
	Detail string `json:"detail,omitempty"`
	// Full is the untruncated text behind Detail, sent so a row can be opened
	// without a second request. Capped, because a tool call can carry a whole
	// file and the list holds hundreds of rows.
	Full string `json:"full,omitempty"`
	// Output is the tool's result, capped the same way. Empty for anything
	// that is not a completed tool call.
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`

	Model       string  `json:"model,omitempty"`
	TokensIn    int     `json:"tokens_in,omitempty"`
	TokensOut   int     `json:"tokens_out,omitempty"`
	CacheRead   int     `json:"cache_read,omitempty"`
	CacheCreate int     `json:"cache_create,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`

	// Thoughts is how many thinking blocks the response carried. Set on
	// KindThinking and on a KindMessage that also reasoned.
	Thoughts int `json:"thoughts,omitempty"`

	// Agent is set on KindSubagent events.
	Agent *AgentRef `json:"agent,omitempty"`

	// toolUseID is the transcript's id for this call. Unexported: it is how a
	// spawned subagent is matched to the Task call that spawned it, and is of
	// no use to the dashboard.
	toolUseID string
}

// AgentRef summarises a spawned subagent. Its own events are fetched
// separately so a session with many agents stays cheap to render.
type AgentRef struct {
	AgentID     string `json:"agent_id"`
	AgentType   string `json:"agent_type,omitempty"`
	Description string `json:"description,omitempty"`
	// Model is the model this agent mostly ran on, which can differ from the
	// parent's when the agent type pins one.
	Model string `json:"model,omitempty"`
	// StartedAt and EndedAt place the agent on a time axis.
	StartedAt  time.Time `json:"started_at,omitzero"`
	EndedAt    time.Time `json:"ended_at,omitzero"`
	SpawnDepth int       `json:"spawn_depth,omitempty"`
	Events     int       `json:"events"`
	ToolCalls  int       `json:"tool_calls"`
	CostUSD    float64   `json:"cost_usd"`
	DurationMS int64     `json:"duration_ms"`
}

// Totals aggregates a timeline.
type Totals struct {
	Events    int `json:"events"`
	ToolCalls int `json:"tool_calls"`
	Errors    int `json:"errors"`
	Subagents int `json:"subagents"`
	// Thinking is how many turns reasoned before acting.
	Thinking int `json:"thinking,omitempty"`
	// Compactions is how many times this session's context was summarised
	// away. A high count on a long session explains a lot of repeated work.
	Compactions int     `json:"compactions"`
	CostUSD     float64 `json:"cost_usd"`
	// ToolMS is time spent inside tool calls and subagents. Calls that ran in
	// parallel are each counted in full, so this is work performed, not wall
	// clock, and legitimately exceeds the session's duration.
	ToolMS int64 `json:"tool_ms"`
}

// Timeline is one session's event stream.
type Timeline struct {
	SessionID string `json:"session_id"`
	Project   string `json:"project,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	// Cwd is the working tree the session ran in, and Repo its basename.
	// Both come from the transcript rather than the encoded directory name,
	// which mangles paths beyond reliable reversal.
	Cwd  string `json:"cwd,omitempty"`
	Repo string `json:"repo,omitempty"`
	// GitBranch is the branch checked out when the session ran.
	GitBranch string `json:"git_branch,omitempty"`
	// Model is the model that did most of the work here.
	Model     string    `json:"model,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	Events    []Event   `json:"events"`
	Totals    Totals    `json:"totals"`
}

// --- transcript shapes ----------------------------------------------------

type line struct {
	Type      string    `json:"type"`
	Subtype   string    `json:"subtype"`
	Timestamp time.Time `json:"timestamp"`
	UUID      string    `json:"uuid"`
	SessionID string    `json:"sessionId"`
	CWD       string    `json:"cwd"`
	GitBranch string    `json:"gitBranch"`
	IsMeta    bool      `json:"isMeta"`
	// CompactMetadata is present on a compact_boundary system line.
	CompactMetadata *struct {
		Trigger                 string `json:"trigger"`
		PreTokens               int    `json:"preTokens"`
		PostTokens              int    `json:"postTokens"`
		CumulativeDroppedTokens int    `json:"cumulativeDroppedTokens"`
		DurationMS              int64  `json:"durationMs"`
	} `json:"compactMetadata"`
	Message *struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	ToolUseID   string `json:"toolUseId"`
	SpawnDepth  int    `json:"spawnDepth"`
}

// --- building -------------------------------------------------------------

// Build reads a session's transcript and returns its timeline, with each
// subagent folded in at the Task call that spawned it.
func Build(claudeDir, sessionID string) (*Timeline, error) {
	path := parser.FindSessionFile(claudeDir, sessionID)
	if path == "" {
		return nil, os.ErrNotExist
	}
	tl, err := buildFromFile(path, sessionID)
	if err != nil {
		return nil, err
	}
	tl.Project = projectName(filepath.Dir(path))
	attachAgents(tl, sessionDir(path, sessionID))
	return tl, nil
}

// BuildAgent returns one subagent's own timeline.
func BuildAgent(claudeDir, sessionID, agentID string) (*Timeline, error) {
	path := parser.FindSessionFile(claudeDir, sessionID)
	if path == "" {
		return nil, os.ErrNotExist
	}
	// agentID comes from a URL; keep it to the shape the files actually use so
	// it can never walk out of the subagents directory.
	if !safeAgentID(agentID) {
		return nil, os.ErrNotExist
	}
	sessDir := sessionDir(path, sessionID)
	agentPath := filepath.Join(sessDir, "subagents", agentID+".jsonl")
	tl, err := buildFromFile(agentPath, sessionID)
	if err != nil {
		return nil, err
	}
	tl.AgentID = agentID
	tl.Project = projectName(filepath.Dir(path))
	// This agent's own subagents live in the same flat session-wide
	// subagents/ directory as everything else — not a directory nested under
	// its own path — so this resolves its Task calls exactly as attachAgents
	// does for the root, rooted here instead.
	foldSubagents(tl, sessDir, agentID, false)
	return tl, nil
}

// safeAgentID accepts only the `agent-<hex>` names Claude Code writes.
func safeAgentID(id string) bool {
	if !strings.HasPrefix(id, "agent-") || len(id) > 64 {
		return false
	}
	for _, r := range id[len("agent-"):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func sessionDir(sessionPath, sessionID string) string {
	return filepath.Join(filepath.Dir(sessionPath), sessionID)
}

func projectName(dir string) string {
	return filepath.Base(dir)
}

func buildFromFile(path, sessionID string) (*Timeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tl := &Timeline{SessionID: sessionID, Events: []Event{}}

	// Pending tool calls, keyed by tool_use id, so a result can close the call
	// it belongs to. Results arrive in a later message, sometimes several
	// messages later when calls run in parallel.
	pending := map[string]int{} // tool_use id -> index in tl.Events

	for _, raw := range strings.Split(string(data), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue
		}
		// Context lines carry cwd and branch even when they hold no message,
		// so read those before skipping anything.
		if tl.Cwd == "" && l.CWD != "" {
			tl.Cwd = l.CWD
			tl.Repo = filepath.Base(l.CWD)
		}
		if tl.GitBranch == "" && l.GitBranch != "" {
			tl.GitBranch = l.GitBranch
		}
		if l.Type == "system" && l.Subtype == "compact_boundary" {
			tl.Events = append(tl.Events, compactEvent(&l))
			continue
		}
		if l.Message == nil || l.IsMeta {
			continue
		}
		blocks := decodeContent(l.Message.Content)

		switch l.Type {
		case "user":
			closeResults(tl, pending, blocks, l.Timestamp)
			if text := userText(blocks); text != "" {
				tl.Events = append(tl.Events, Event{
					Kind:   KindPrompt,
					At:     l.Timestamp,
					Title:  "You",
					Detail: summarize(text),
					Full:   clip(text),
				})
			}
		case "assistant":
			appendAssistant(tl, pending, &l, blocks)
		}
	}

	sort.SliceStable(tl.Events, func(i, j int) bool {
		return tl.Events[i].At.Before(tl.Events[j].At)
	})
	mergeThinking(tl)
	describeThinking(tl)
	finalize(tl)
	return tl, nil
}

// appendAssistant emits the assistant's prose and each tool call it made.
//
// Token usage is reported once per API response, so it is attributed to the
// first event this message produces rather than duplicated across its tool
// calls.
func appendAssistant(tl *Timeline, pending map[string]int, l *line, blocks []contentBlock) {
	var text strings.Builder
	var tools []contentBlock
	thoughts := 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			tools = append(tools, b)
		case "thinking", "redacted_thinking":
			thoughts++
		}
	}

	usage := Event{Model: l.Message.Model}
	if u := l.Message.Usage; u != nil {
		usage.TokensIn = u.InputTokens
		usage.TokensOut = u.OutputTokens
		usage.CacheRead = u.CacheReadInputTokens
		usage.CacheCreate = u.CacheCreationInputTokens
		usage.CostUSD = parser.MessageCostUSD(l.Message.Model,
			u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	}
	charged := false

	if t := strings.TrimSpace(text.String()); t != "" {
		ev := Event{Kind: KindMessage, At: l.Timestamp, Title: "Claude", Detail: summarize(t), Full: clip(t)}
		// A response that reasoned and then spoke: the words are the summary,
		// and the thought count is a badge on them rather than its own row.
		ev.Thoughts = thoughts
		applyUsage(&ev, usage)
		charged = true
		tl.Events = append(tl.Events, ev)
	} else if thoughts > 0 {
		// Reasoned and went straight to tools, which is the common shape. Emit
		// a row so the calls below it have a parent; its detail is filled in
		// from the burst it produced once the events are all in order.
		ev := Event{
			Kind:     KindThinking,
			At:       l.Timestamp,
			Title:    "Thinking",
			Thoughts: thoughts,
		}
		applyUsage(&ev, usage)
		charged = true
		tl.Events = append(tl.Events, ev)
	}

	for _, b := range tools {
		detail, full := toolDetail(b.Name, b.Input)
		ev := Event{
			Kind:   KindTool,
			At:     l.Timestamp,
			Title:  b.Name,
			Detail: detail,
			Full:   full,
		}
		if !charged {
			applyUsage(&ev, usage)
			charged = true
		}
		ev.toolUseID = b.ID
		tl.Events = append(tl.Events, ev)
		if b.ID != "" {
			pending[b.ID] = len(tl.Events) - 1
		}
	}
}

func applyUsage(ev *Event, u Event) {
	ev.Model = u.Model
	ev.TokensIn = u.TokensIn
	ev.TokensOut = u.TokensOut
	ev.CacheRead = u.CacheRead
	ev.CacheCreate = u.CacheCreate
	ev.CostUSD = u.CostUSD
}

// closeResults stamps duration and error state onto the tool calls whose
// results appear in this user message.
func closeResults(tl *Timeline, pending map[string]int, blocks []contentBlock, at time.Time) {
	for _, b := range blocks {
		if b.Type != "tool_result" || b.ToolUseID == "" {
			continue
		}
		idx, ok := pending[b.ToolUseID]
		if !ok {
			continue
		}
		delete(pending, b.ToolUseID)
		ev := &tl.Events[idx]
		if !at.IsZero() && at.After(ev.At) {
			ev.DurationMS = at.Sub(ev.At).Milliseconds()
		}
		ev.IsError = b.IsError
		ev.Output = clip(resultText(b.Content))
	}
}

// attachAgents folds each spawned subagent into the Task event that spawned
// it, nesting a subagent's own subagents beneath it in turn. An agent whose
// parent Task call is missing from the transcript is appended at its own
// start time rather than dropped.
func attachAgents(tl *Timeline, sessDir string) {
	foldSubagents(tl, sessDir, "", true)
}

// foldSubagents is the shared implementation behind attachAgents (rooted at
// a session) and BuildAgent (rooted at one agent's own transcript). All of a
// session's agents, at every spawn depth, live flat in the one
// sessDir/subagents directory — see the package doc comment — so both
// callers read the same directory and share the same pool.
//
// excludeID leaves one agent out of the pool: BuildAgent builds tl from that
// agent's own file, and it must not be able to match itself.
//
// withLeftovers runs the "never drop a spawned agent" fallback (an agent
// whose toolUseId never matched anywhere gets pinned onto tl regardless).
// That guarantee is about the session as a whole, not about whichever
// agent happens to be the current recursion root — so only attachAgents
// (the true session root) sets this. Without it, every unrelated
// leftover agent in the session would land under every single expanded
// agent's own view via BuildAgent instead of once, at the root.
func foldSubagents(tl *Timeline, sessDir, excludeID string, withLeftovers bool) {
	agents := discoverAgents(sessDir, tl.SessionID, excludeID)
	if len(agents) == 0 {
		return
	}
	pool := newAgentPool(agents)
	foldChildren(tl, pool)
	if withLeftovers {
		attachLeftovers(tl, pool)
	}
	sort.SliceStable(tl.Events, func(i, j int) bool { return tl.Events[i].At.Before(tl.Events[j].At) })
	finalize(tl)
}

// foundAgent is one subagent read off disk, still unattached to any tree.
type foundAgent struct {
	agentID string
	meta    agentMeta
	sub     *Timeline
}

// discoverAgents reads every subagent transcript in a session's flat
// subagents/ directory, regardless of spawn depth — a depth-2 agent's file
// sits in exactly the same directory as a depth-1 one.
func discoverAgents(sessDir, sessionID, excludeID string) []*foundAgent {
	dir := filepath.Join(sessDir, "subagents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var agents []*foundAgent
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		agentID := strings.TrimSuffix(name, ".jsonl")
		if agentID == excludeID {
			continue
		}
		meta := readMeta(filepath.Join(dir, agentID+".meta.json"))
		sub, err := buildFromFile(filepath.Join(dir, name), sessionID)
		if err != nil {
			continue
		}
		agents = append(agents, &foundAgent{agentID: agentID, meta: meta, sub: sub})
	}
	return agents
}

// agentPool is a session-wide toolUseId -> agent pool. A tool-use id can
// only be claimed once anywhere in the tree, so matching drains it: this
// makes the depth-first fold below terminate and rules out double-attaching
// an agent or looping.
type agentPool struct {
	byToolUse map[string]*foundAgent
	byID      map[string]*foundAgent
}

func newAgentPool(agents []*foundAgent) *agentPool {
	p := &agentPool{byToolUse: map[string]*foundAgent{}, byID: map[string]*foundAgent{}}
	for _, a := range agents {
		p.byID[a.agentID] = a
		if a.meta.ToolUseID != "" {
			p.byToolUse[a.meta.ToolUseID] = a
		}
	}
	return p
}

// take claims the agent whose meta names toolUseID as its spawning call.
func (p *agentPool) take(toolUseID string) (*foundAgent, bool) {
	a, ok := p.byToolUse[toolUseID]
	if !ok {
		return nil, false
	}
	delete(p.byToolUse, toolUseID)
	delete(p.byID, a.agentID)
	return a, true
}

// takeByID claims an agent by its own id, used by the leftover fallback
// below. Returns false if the agent was already claimed as someone's child
// while an earlier leftover's own subtree was folded.
func (p *agentPool) takeByID(agentID string) (*foundAgent, bool) {
	a, ok := p.byID[agentID]
	if !ok {
		return nil, false
	}
	delete(p.byID, agentID)
	if a.meta.ToolUseID != "" {
		delete(p.byToolUse, a.meta.ToolUseID)
	}
	return a, true
}

// remainingOldestFirst names every agent still unclaimed, oldest start
// first — the order the leftover fallback pairs and appends in.
func (p *agentPool) remainingOldestFirst() []string {
	ids := make([]string, 0, len(p.byID))
	for id := range p.byID {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		return p.byID[ids[i]].sub.StartedAt.Before(p.byID[ids[j]].sub.StartedAt)
	})
	return ids
}

// foldChildren matches tl's own Task calls against the shared pool. Each
// resolved agent is recursed into — matching its own Task calls against the
// same, now-smaller pool — and finalized before tl's own event is built, so
// a grandchild's cost and tool-call totals roll up through finalize (see
// its doc comment) before the parent's AgentRef reads them. This is what
// makes rollup work through arbitrarily many levels with no separate
// rollup code.
func foldChildren(tl *Timeline, pool *agentPool) {
	for i := range tl.Events {
		ev := &tl.Events[i]
		if ev.Kind != KindTool || ev.Title != "Task" || ev.toolUseID == "" {
			continue
		}
		a, ok := pool.take(ev.toolUseID)
		if !ok {
			continue
		}
		foldChildren(a.sub, pool)
		finalize(a.sub)
		foldEvent(ev, buildAgentRef(a))
	}
}

// attachLeftovers places every agent whose toolUseId never matched a Task
// call anywhere in the tree: a stale/missing meta file, not a genuine child
// of something already resolved. It pairs each with whatever root Task call
// is still unclaimed, oldest first, then appends anything left as an orphan
// so a spawned agent is never silently dropped. An agent claimed as someone
// else's child while an earlier leftover's own subtree was folded is
// skipped — it already has a home.
func attachLeftovers(tl *Timeline, pool *agentPool) {
	order := pool.remainingOldestFirst()
	var unpaired []int
	for i, ev := range tl.Events {
		if ev.Kind == KindTool && ev.Title == "Task" {
			unpaired = append(unpaired, i)
		}
	}
	for _, agentID := range order {
		a, ok := pool.takeByID(agentID)
		if !ok {
			continue
		}
		foldChildren(a.sub, pool)
		finalize(a.sub)
		ref := buildAgentRef(a)
		if len(unpaired) > 0 {
			foldEvent(&tl.Events[unpaired[0]], ref)
			unpaired = unpaired[1:]
			continue
		}
		tl.Events = append(tl.Events, Event{
			Kind:       KindSubagent,
			At:         a.sub.StartedAt,
			Title:      firstNonEmpty(ref.AgentType, "subagent"),
			Detail:     ref.Description,
			DurationMS: ref.DurationMS,
			CostUSD:    ref.CostUSD,
			Agent:      &ref,
		})
	}
}

// buildAgentRef summarises a resolved agent. Called after the agent's own
// children have been folded and finalized, so Events/ToolCalls/CostUSD
// already include whatever it spawned in turn.
func buildAgentRef(a *foundAgent) AgentRef {
	sub := a.sub
	ref := AgentRef{
		AgentID:     a.agentID,
		AgentType:   a.meta.AgentType,
		Description: a.meta.Description,
		Model:       sub.Model,
		SpawnDepth:  a.meta.SpawnDepth,
		StartedAt:   sub.StartedAt,
		EndedAt:     sub.EndedAt,
		Events:      sub.Totals.Events,
		ToolCalls:   sub.Totals.ToolCalls,
		CostUSD:     sub.Totals.CostUSD,
	}
	if !sub.StartedAt.IsZero() && sub.EndedAt.After(sub.StartedAt) {
		ref.DurationMS = sub.EndedAt.Sub(sub.StartedAt).Milliseconds()
	}
	return ref
}

// foldEvent turns a Task tool call into the subagent event it spawned.
func foldEvent(ev *Event, ref AgentRef) {
	ev.Kind = KindSubagent
	r := ref
	ev.Agent = &r
	if r.AgentType != "" {
		ev.Title = r.AgentType
	}
	if r.Description != "" {
		ev.Detail = r.Description
	}
	if ev.DurationMS == 0 {
		ev.DurationMS = r.DurationMS
	}
}

func readMeta(path string) agentMeta {
	var m agentMeta
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// compactEvent describes a context compaction in the terms that matter: what
// triggered it, and how much history it dropped.
func compactEvent(l *line) Event {
	ev := Event{
		Kind:  KindCompact,
		At:    l.Timestamp,
		Title: "Compacted",
	}
	m := l.CompactMetadata
	if m == nil {
		return ev
	}
	trigger := m.Trigger
	if trigger == "" {
		trigger = "unknown"
	}
	ev.DurationMS = m.DurationMS
	dropped := m.PreTokens - m.PostTokens
	if dropped < 0 {
		dropped = 0
	}
	ev.Detail = fmt.Sprintf("%s · %s → %s tokens, %s dropped",
		trigger, humanCount(m.PreTokens), humanCount(m.PostTokens), humanCount(dropped))
	ev.Full = fmt.Sprintf(
		"trigger: %s\nbefore: %d tokens\nafter: %d tokens\ndropped: %d tokens\ncumulative dropped: %d tokens\ntook: %s",
		trigger, m.PreTokens, m.PostTokens, dropped, m.CumulativeDroppedTokens,
		(time.Duration(m.DurationMS) * time.Millisecond).Round(time.Second))
	return ev
}

// humanCount abbreviates a token count; exact figures live in the expansion.
func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// mergeThinking attaches each thinking row to the response it belongs to.
//
// A turn is written across several assistant lines: the reasoning on its own,
// then the text, then the tool calls. Read per line that becomes three rows for
// one turn, with the reasoning row apparently doing nothing.
//
// So a thinking row folds forward into the message that follows it — the words
// become the summary of what the turn was pursuing, carrying a count of the
// thoughts behind them, and the tool calls sit beneath. A turn that reasoned and
// went straight to tools keeps its own row, because those calls would otherwise
// have no parent.
func mergeThinking(tl *Timeline) {
	out := make([]Event, 0, len(tl.Events))
	for i := 0; i < len(tl.Events); i++ {
		ev := tl.Events[i]
		if ev.Kind == KindThinking && i+1 < len(tl.Events) && tl.Events[i+1].Kind == KindMessage {
			next := &tl.Events[i+1]
			next.Thoughts += ev.Thoughts
			// The reasoning and the words are one response as far as spend is
			// concerned; keep the total on the row that survives.
			next.TokensIn += ev.TokensIn
			next.TokensOut += ev.TokensOut
			next.CacheRead += ev.CacheRead
			next.CacheCreate += ev.CacheCreate
			next.CostUSD += ev.CostUSD
			// Keep the earlier time: the turn began when it started thinking.
			if ev.At.Before(next.At) {
				next.At = ev.At
			}
			continue
		}
		// Consecutive fragments with nothing between them are still one thought.
		if ev.Kind == KindThinking && len(out) > 0 && out[len(out)-1].Kind == KindThinking {
			prev := &out[len(out)-1]
			prev.Thoughts += ev.Thoughts
			prev.TokensIn += ev.TokensIn
			prev.TokensOut += ev.TokensOut
			prev.CacheRead += ev.CacheRead
			prev.CacheCreate += ev.CacheCreate
			prev.CostUSD += ev.CostUSD
			continue
		}
		out = append(out, ev)
	}
	tl.Events = out
}

// describeThinking labels each thinking row with the work it produced.
//
// The reasoning text is not in the transcript, so the honest summary is what
// the turn actually did: the tools it ran. Stated as a derivation rather than
// dressed up as the model's own words.
func describeThinking(tl *Timeline) {
	for i := range tl.Events {
		if tl.Events[i].Kind != KindThinking {
			continue
		}
		var names []string
		errs := 0
		for j := i + 1; j < len(tl.Events); j++ {
			if tl.Events[j].Kind != KindTool {
				break
			}
			if tl.Events[j].IsError {
				errs++
			}
			// Repeats collapse: "Bash ×4" reads better than four Bashes.
			if len(names) == 0 || names[len(names)-1] != tl.Events[j].Title {
				names = append(names, tl.Events[j].Title)
			}
		}
		ev := &tl.Events[i]
		switch {
		case len(names) == 0:
			ev.Detail = "reasoned, no tool calls"
		case len(names) <= 4:
			ev.Detail = "then " + join(names, ", ")
		default:
			ev.Detail = "then " + join(names[:4], ", ") + fmt.Sprintf(" +%d more", len(names)-4)
		}
		if errs > 0 {
			ev.Detail += fmt.Sprintf(" · %d failed", errs)
		}
		ev.Full = fmt.Sprintf(
			"%d thinking block(s) in this turn.\n\n"+
				"The reasoning text is not available. Claude Code records thinking "+
				"blocks with an empty body and a signature only, so what the terminal "+
				"shows live is never written to the transcript.\n\n"+
				"What this turn did: %s",
			ev.Thoughts, ev.Detail)
	}
}

func join(xs []string, sep string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += sep
		}
		out += x
	}
	return out
}

// dominantModel is the model that produced the most events here. A session can
// switch models mid-run; the tooltip wants the one that did the work, not the
// last one seen.
func dominantModel(events []Event) string {
	counts := map[string]int{}
	for _, e := range events {
		if e.Model != "" {
			counts[e.Model]++
		}
	}
	best, bestN := "", 0
	for m, n := range counts {
		// Ties break on the name so the answer is stable across runs; Go map
		// iteration order is not.
		if n > bestN || (n == bestN && m < best) {
			best, bestN = m, n
		}
	}
	return best
}

// finalize renumbers events and recomputes the totals.
func finalize(tl *Timeline) {
	var t Totals
	for i := range tl.Events {
		ev := &tl.Events[i]
		ev.Seq = i
		t.Events++
		t.CostUSD += ev.CostUSD
		switch ev.Kind {
		case KindTool:
			t.ToolCalls++
			t.ToolMS += ev.DurationMS
		case KindThinking:
			t.Thinking++
		case KindCompact:
			t.Compactions++
		case KindSubagent:
			t.Subagents++
			t.ToolMS += ev.DurationMS
			if ev.Agent != nil {
				// A subagent's own spend belongs in the session total; it is
				// not reported anywhere in the parent transcript.
				t.CostUSD += ev.Agent.CostUSD
			}
		}
		if ev.IsError {
			t.Errors++
		}
	}
	if len(tl.Events) > 0 {
		tl.StartedAt = tl.Events[0].At
		// The last event is not necessarily the one that ends latest: a long
		// tool call started earlier can outlast it.
		end := tl.Events[0].At
		for _, ev := range tl.Events {
			if fin := ev.At.Add(time.Duration(ev.DurationMS) * time.Millisecond); fin.After(end) {
				end = fin
			}
		}
		tl.EndedAt = end
	}
	tl.Model = dominantModel(tl.Events)
	tl.Totals = t
}

// --- content helpers ------------------------------------------------------

// decodeContent handles both content shapes: a plain string, or the block
// array. Older transcripts use the former for user messages.
func decodeContent(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []contentBlock{{Type: "text", Text: text}}
	}
	return nil
}

// resultText flattens a tool_result payload, which is either a plain string or
// an array of content blocks depending on the tool.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Text != "" {
			b.WriteString(blk.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func userText(blocks []contentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// Longest text carried per event. A row can be opened to read the whole
// argument or result, but a transcript can hold a megabyte in one tool call
// and the list renders hundreds of rows.
const maxFullChars = 4000

// toolDetail picks the argument that says what the call was actually doing,
// returning both the one-line summary and the untruncated text.
func toolDetail(name string, input json.RawMessage) (summary, full string) {
	if len(input) == 0 {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return "", ""
	}
	// Ordered by how much each says about the call, most specific first.
	for _, key := range []string{
		"command", "file_path", "pattern", "path", "url", "query",
		"description", "prompt", "notebook_path", "skill",
	} {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return summarize(v), clip(strings.TrimSpace(v))
		}
	}
	return "", ""
}

// clip bounds a string on a rune boundary.
func clip(s string) string {
	if len(s) <= maxFullChars {
		return s
	}
	cut := maxFullChars
	for cut > 0 && !isBoundary(s, cut) {
		cut--
	}
	return s[:cut] + "\n…(truncated)"
}

// summarize collapses text to one short line. Timelines are scanned, not read.
func summarize(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 120
	if len(s) > max {
		// Trim on a rune boundary so a multi-byte character is never split.
		cut := max
		for cut > 0 && !isBoundary(s, cut) {
			cut--
		}
		s = strings.TrimSpace(s[:cut]) + "…"
	}
	return s
}

func isBoundary(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	return s[i]&0xC0 != 0x80
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
