package timeline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fabioconcina/claumon/internal/herdr"
	"github.com/fabioconcina/claumon/internal/nimbalyst"
	"github.com/fabioconcina/claumon/internal/parser"
)

// Fleet answers a different question from Build: not "what did this session
// do", but "what was running, when".
//
// The unit is the Claude session, wherever it ran. A project directory is not
// the unit — a general workspace can host dozens of unrelated sessions, and
// grouping by directory buries them. Each session is a row; the agents it
// spawned are its children, placed at the moment they were spawned and drawn
// for as long as they ran.
//
// Spans are derived without parsing transcripts in full. Session bounds come
// from the summary claumon already caches; an agent's start is the first line
// of its transcript and its end is that file's last write. Reading two lines
// and a stat per agent keeps a week-wide view cheap, where full parsing of
// every agent in every session would not be.
type Fleet struct {
	From     time.Time      `json:"from"`
	To       time.Time      `json:"to"`
	Sessions []FleetSession `json:"sessions"`
}

// FleetSession is one Claude session inside the window.
type FleetSession struct {
	SessionID string `json:"session_id"`
	// Repo is the working tree's basename — the name a person uses for it.
	Repo string `json:"repo,omitempty"`
	// Worktree is the linked git worktree's name, empty for a main checkout.
	// Two sessions in the same repo on different worktrees are different work
	// and share a cwd basename, so this is what tells them apart.
	Worktree  string `json:"worktree,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	GitBranch string `json:"git_branch,omitempty"`
	Model     string `json:"model,omitempty"`
	Title     string `json:"title,omitempty"`

	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	CostUSD   float64   `json:"cost_usd"`
	Messages  int       `json:"messages"`
	// Tokens is everything the session moved through the model, cache reads
	// included. Used for the heat view, where the question is burn rate rather
	// than billable spend.
	Tokens int `json:"tokens"`
	// Burn is tokens per equal time bucket across the session's span, filled
	// only when the caller asks for heat data. It is what lets a bar be shaded
	// along its length instead of averaged to one colour.
	Burn []int `json:"burn,omitempty"`
	// Spans is the session's life broken into what was actually happening:
	// stretches of work, lulls past the cache TTL, and — as the gaps between
	// them — the absences where the session was put down and picked back up.
	// A bar drawn from Spans says when work happened; one drawn from
	// StartedAt to EndedAt only says the session existed.
	Spans     []Span `json:"spans,omitempty"`
	IsRunning bool   `json:"is_running"`

	Agents []FleetAgent `json:"agents,omitempty"`

	// Herdr is the pane this session is running in, when the workspace
	// manager knows about it. Absent when herdr is not running.
	Herdr *HerdrRef `json:"herdr,omitempty"`

	// Nimbalyst is the app-side view of this session, when it was started
	// from Nimbalyst rather than a terminal. A session runs in one host or the
	// other, so in practice this and Herdr are mutually exclusive — but
	// nothing enforces that, and both are shown if both claim it.
	Nimbalyst *NimbalystRef `json:"nimbalyst,omitempty"`

	// ForkedFrom names the session this one was split off from, when it was.
	ForkedFrom *ForkRef `json:"forked_from,omitempty"`
}

// ForkRef is a session's lineage.
//
// Claude Code records this two different ways depending on how the split was
// made, and both are read:
//
//   - /branch stamps every inherited line with forkedFrom{sessionId,
//     messageUuid}, naming the exact message the split happened at.
//   - /fork copies the history without that stamp, but the copied lines keep
//     the parent's `session_id` while `sessionId` is rewritten to the new one.
//     The mismatch is the only trace, so it is treated as one.
type ForkRef struct {
	SessionID string `json:"session_id"`
	// MessageUUID is the message the split was taken at. Only /branch records
	// it; empty for a /fork.
	MessageUUID string `json:"message_uuid,omitempty"`
	// Inferred is true when the lineage came from the session_id mismatch
	// rather than an explicit field, so a reader knows how firm it is.
	Inferred bool `json:"inferred,omitempty"`
	// DivergedAt is when this session stopped replaying inherited history and
	// began its own work. Without it a child's bar spans its parent's entire
	// past, because a split copies the transcript with the original
	// timestamps — the two would look like duplicates rather than a branch.
	DivergedAt time.Time `json:"diverged_at,omitzero"`
}

// HerdrRef locates a session in the terminal workspace manager, so the
// dashboard can name the task the way the user named it and put the pane in
// front of them.
type HerdrRef struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Title is the tab's task name — what the user called this piece of work,
	// which names a thread far better than any directory can.
	Title string `json:"title,omitempty"`
	// Status is herdr's live view: working or idle. More reliable than
	// inferring from file mtimes, because herdr watches the process.
	Status  string `json:"status,omitempty"`
	Focused bool   `json:"focused,omitempty"`
}

// NimbalystRef locates a session inside the Nimbalyst desktop app, so the
// dashboard can name it the way the user named it there.
//
// Unlike HerdrRef there is no pane to focus: Nimbalyst has no deep link that
// selects a session, and its MCP port needs a token no other process can
// obtain. RevealURL therefore only raises the app, and the UI says so.
type NimbalystRef struct {
	// SessionID is Nimbalyst's own id, which its UI addresses the session by.
	SessionID string `json:"session_id"`
	// Workspace is the project directory, and WorkspaceName its basename —
	// the name a person uses for that project.
	Workspace     string `json:"workspace,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	// Title is the session name shown in Nimbalyst.
	Title string `json:"title,omitempty"`
	// Status is Nimbalyst's live view: idle, running, waiting_for_input, or
	// error. waiting_for_input is one claumon cannot infer at all.
	Status string `json:"status,omitempty"`
	// Worktree is the linked worktree's display name, when there is one.
	Worktree string `json:"worktree,omitempty"`
	// Model is Nimbalyst's own alias, e.g. "claude-code:haiku". It says which
	// alias the user picked, which the transcript does not record.
	Model string `json:"model,omitempty"`
	// RevealURL raises Nimbalyst. It cannot select the session; see the type
	// comment.
	RevealURL string `json:"reveal_url,omitempty"`
}

// FleetAgent is one subagent, as a span.
type FleetAgent struct {
	AgentID     string    `json:"agent_id"`
	AgentType   string    `json:"agent_type,omitempty"`
	Description string    `json:"description,omitempty"`
	SpawnDepth  int       `json:"spawn_depth,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
}

// BuildFleet returns every session overlapping [from, to], newest first.
//
// scanLimit bounds how many recent sessions are considered before filtering.
// A window is a time range, but the transcripts on disk are not indexed by
// time, so the scan is bounded by recency and then filtered.
// BuildFleet returns the window's sessions. withBurn adds a per-session token
// series, which costs a full transcript read per session and is therefore opt-in.
func BuildFleet(claudeDir string, from, to time.Time, scanLimit int, withBurn bool) (*Fleet, error) {
	// One call, reused for every session: herdr is asked once per request
	// rather than once per row. Failure is silent by design — the fleet is
	// complete without it.
	panes := map[string]herdr.Agent{}
	if agents, err := (herdr.Client{}).List(); err == nil {
		panes = herdr.BySession(agents)
	}

	// The same treatment as herdr: asked once per request, and a failure means
	// no enrichment rather than a failed request. Nimbalyst is usually not
	// running at all, which is indistinguishable from it having no sessions.
	nimbaly := map[string]nimbalyst.Session{}
	if sess, err := (nimbalyst.Client{}).List(); err == nil {
		nimbaly = nimbalyst.BySession(sess)
	}

	// Whether a session still exists is a question about processes, not about
	// transcripts. Claude Code writes ~/.claude/sessions/{PID}.json for each
	// live session, and BuildProcessMap keeps only the entries whose process is
	// actually running — so a session in this map is alive and one absent from
	// it has ended, whatever its file mtimes suggest.
	live := parser.BuildProcessMap(claudeDir)

	if scanLimit <= 0 {
		scanLimit = 500
	}
	summaries, err := parser.DiscoverRecentSessions(claudeDir, scanLimit)
	if err != nil {
		// A machine that has never run Claude Code has no projects directory.
		// That is an empty window, not a failure: returning an error here puts
		// a 500 on the dashboard for a first-run install.
		if errors.Is(err, os.ErrNotExist) {
			return &Fleet{From: from, To: to, Sessions: []FleetSession{}}, nil
		}
		return nil, err
	}

	out := &Fleet{From: from, To: to, Sessions: []FleetSession{}}
	for _, s := range summaries {
		if s == nil {
			continue
		}
		start, end := s.StartTime, s.LastActivity
		if end.Before(start) {
			end = start
		}
		// Overlap, not containment: a session that started before the window
		// and is still running belongs in it.
		if start.After(to) || end.Before(from) {
			continue
		}

		path := parser.FindSessionFile(claudeDir, s.ID)
		cwd, branch := s.CWD, ""
		var forked *ForkRef
		if path != "" {
			if c, b, ok := sessionContext(path); ok {
				if c != "" {
					cwd = c
				}
				branch = b
			}
			forked = detectFork(path, s.ID)
			if forked != nil {
				forked.DivergedAt = divergedAt(path, s.ID, forked)
			}
		}

		// The PID file is the authority. A summary's own IsRunning is inferred
		// from file activity, which calls a session that has been quiet for an
		// hour dead and one whose transcript was just touched alive — neither
		// of which is the question.
		_, alive := live[s.ID]

		fs := FleetSession{
			SessionID:  s.ID,
			Cwd:        cwd,
			GitBranch:  branch,
			Model:      s.Model,
			Title:      s.Title,
			StartedAt:  start,
			EndedAt:    end,
			CostUSD:    s.EstimatedCostUSD,
			Tokens:     s.InputTokens + s.OutputTokens + s.CacheReadTokens + s.CacheCreateTokens,
			Messages:   s.MessageCount,
			IsRunning:  alive,
			ForkedFrom: forked,
		}
		if cwd != "" {
			// The cwd basename stays the location's name: a session run in a
			// module or subdirectory is best named by that directory, not by
			// the repository root far above it. The worktree is added
			// alongside, because that is the part the basename cannot show —
			// two checkouts of one repo have the same basename.
			fs.Repo = repoLabel(cwd)
			fs.Worktree = worktreeOf(cwd).Name
		} else if s.Project != "" {
			fs.Repo = filepath.Base(s.Project)
		}
		if path != "" {
			fs.Agents = agentSpans(sessionDir(path, s.ID))
			if withBurn {
				fs.Burn = BurnSeries(path, start, end)
			}
		}
		if h, ok := panes[s.ID]; ok {
			fs.Herdr = &HerdrRef{
				PaneID:      h.PaneID,
				TabID:       h.TabID,
				WorkspaceID: h.WorkspaceID,
				Title:       h.Title,
				Status:      h.Status,
				Focused:     h.Focused,
			}

			// herdr's tab title is the name the user gave this task. It beats
			// anything derived from the transcript, so it wins outright rather
			// than only filling a gap.
			if h.Title != "" {
				fs.Title = h.Title
			}
		}
		// Spans are computed after herdr has had its say: whether a session is
		// still open decides whether its quiet tail is drawn as an idle
		// stretch, and herdr's view of that beats the mtime inference.
		if path != "" {
			// A running session's idle tail runs to now, not to the window's
			// far edge — a week-wide window would otherwise draw a dashed
			// stretch days into the future.
			tail := to
			if now := time.Now().UTC(); now.Before(tail) {
				tail = now
			}
			fs.Spans = Activity(path, tail, fs.IsRunning)
		}

		if n, ok := nimbaly[s.ID]; ok {
			fs.Nimbalyst = &NimbalystRef{
				SessionID:     n.ID,
				Workspace:     n.Workspace,
				WorkspaceName: n.WorkspaceName(),
				Title:         n.Title,
				Status:        n.Status,
				Worktree:      n.Worktree,
				Model:         n.Model,
				RevealURL:     nimbalyst.RevealURL(n),
			}
			// The name the user gave the session in Nimbalyst is the best one
			// available, on the same reasoning as herdr's tab title: it beats
			// a title derived from the first user message. Nimbalyst's default
			// "New Session" is not a name, so it does not win.
			if n.Title != "" && n.Title != "New Session" {
				fs.Title = n.Title
			}
			// Nimbalyst watches the session itself, so its status settles what
			// the transcript cannot: a session waiting on a prompt is neither
			// working nor finished.
			if n.Status == "running" || n.Status == "waiting_for_input" {
				fs.IsRunning = true
			}
			// A Nimbalyst worktree is the same fact as a git worktree, found a
			// different way. Prefer what git says, since that is read from the
			// checkout itself.
			if fs.Worktree == "" {
				fs.Worktree = n.Worktree
			}
			// A session launched from Nimbalyst has no cwd of its own in the
			// transcript when it never ran a shell, so the workspace names it.
			if fs.Repo == "" {
				fs.Repo = n.WorkspaceName()
			}
		}

		// A session's own title falls back to its first user message, which
		// for a session resumed from a local command is the harness's caveat
		// preamble rather than anything the user said. That text is identical
		// across every such session, so it names nothing — drop it and let the
		// location label the row.
		if isBoilerplateTitle(fs.Title) {
			fs.Title = ""
		}
		out.Sessions = append(out.Sessions, fs)
	}

	sortFleet(out.Sessions)

	return out, nil
}

// detectFork reads a session's lineage from the head of its transcript.
//
// Bounded like sessionContext: a split copies the whole history, so the trace
// appears within the first handful of lines, and these files reach tens of
// megabytes. Reading them in full for one field would make a week-wide view
// unusable.
func detectFork(path, ownID string) *ForkRef {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var inferred *ForkRef
	for i := 0; i < 200 && sc.Scan(); i++ {
		var l struct {
			SessionIDSnake string `json:"session_id"`
			ForkedFrom     *struct {
				SessionID   string `json:"sessionId"`
				MessageUUID string `json:"messageUuid"`
			} `json:"forkedFrom"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		// Explicit lineage wins and names the split point, so stop there.
		if l.ForkedFrom != nil && l.ForkedFrom.SessionID != "" && l.ForkedFrom.SessionID != ownID {
			return &ForkRef{
				SessionID:   l.ForkedFrom.SessionID,
				MessageUUID: l.ForkedFrom.MessageUUID,
			}
		}
		// Otherwise remember the mismatch, but keep looking for the explicit
		// field: a /branch also carries inherited session_ids.
		if inferred == nil && l.SessionIDSnake != "" && l.SessionIDSnake != ownID {
			inferred = &ForkRef{SessionID: l.SessionIDSnake, Inferred: true}
		}
	}
	return inferred
}

// divergedAt finds when a split session's own work begins.
//
// The whole file is walked, not a tail window. An earlier version seeked to the
// last 4MB on the reasoning that a split's own work sits at the end — but the
// inherited history is what is long, and a session that branches early then
// works for a day puts the boundary tens of megabytes from the end. Landing
// past it meant seeing no inherited line at all, so the first line in the
// window looked like the start of own work and the divergence was reported as
// minutes ago. The bar then drew almost the whole child as inherited history
// with a divergence pinned near the right edge, which is exactly backwards.
//
// The cost is one sequential pass with no JSON decoding on the common line:
// the marker is found by byte, and only a line whose class has changed is
// decoded for its timestamp.
//
// The signal differs by route, matching how each is recorded:
//
//   - explicit (/branch): inherited lines carry forkedFrom, own lines do not.
//   - inferred (/fork): inherited lines carry the parent's session_id, own
//     lines carry this session's.
func divergedAt(path, ownID string, ref *ForkRef) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	forkedKey := []byte(`"forkedFrom":`)
	parentKey := []byte(`"session_id":"` + ref.SessionID + `"`)
	ownKey := []byte(`"session_id":"` + ownID + `"`)

	// Walk forward, keeping the earliest own line seen after the last inherited
	// one. Resetting on each inherited line is what handles interleaving: only
	// the final run of own lines counts.
	var earliestOwn time.Time
	for sc.Scan() {
		b := sc.Bytes()

		// Classify by byte. Every line is tested; only the ones that could
		// move the boundary are decoded.
		inherited := false
		if ref.Inferred {
			inherited = bytes.Contains(b, parentKey)
		} else {
			inherited = bytes.Contains(b, forkedKey) &&
				!bytes.Contains(b, []byte(`"forkedFrom":null`))
		}
		if inherited {
			earliestOwn = time.Time{}
			continue
		}
		if ref.Inferred && !bytes.Contains(b, ownKey) {
			// Bookkeeping lines carry no session id; they are neither
			// inherited nor evidence of new work.
			continue
		}
		// A candidate for the boundary. Only now is decoding worth it, and
		// only the first of the current run counts.
		if !earliestOwn.IsZero() {
			continue
		}
		var l struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(b, &l) != nil || l.Timestamp.IsZero() {
			continue
		}
		if earliestOwn.IsZero() {
			earliestOwn = l.Timestamp
		}
	}
	return earliestOwn
}

// sessionContext reads the working tree and branch from the transcript's first
// informative line, without parsing the file.
func sessionContext(path string) (cwd, branch string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	// Context appears on early lines; a handful is enough, and bounding the
	// scan keeps a huge transcript from being read for two fields.
	for i := 0; i < 40 && sc.Scan(); i++ {
		var l struct {
			CWD       string `json:"cwd"`
			GitBranch string `json:"gitBranch"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if cwd == "" {
			cwd = l.CWD
		}
		if branch == "" {
			branch = l.GitBranch
		}
		if cwd != "" && branch != "" {
			break
		}
	}
	return cwd, branch, cwd != "" || branch != ""
}

// agentSpans lists the subagents of a session as time spans.
func agentSpans(sessDir string) []FleetAgent {
	dir := filepath.Join(sessDir, "subagents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []FleetAgent
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		agentID := strings.TrimSuffix(name, ".jsonl")
		path := filepath.Join(dir, name)

		start, ok := firstTimestamp(path)
		if !ok {
			continue
		}
		end := start
		if info, err := e.Info(); err == nil {
			// The last write is when the agent last produced anything, which
			// is the closest thing to a finish time without reading the file.
			if mt := info.ModTime(); mt.After(end) {
				end = mt
			}
		}

		meta := readMeta(filepath.Join(dir, agentID+".meta.json"))
		out = append(out, FleetAgent{
			AgentID:     agentID,
			AgentType:   meta.AgentType,
			Description: meta.Description,
			SpawnDepth:  meta.SpawnDepth,
			StartedAt:   start,
			EndedAt:     end,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// firstTimestamp reads the first timestamped line of a transcript.
func firstTimestamp(path string) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for i := 0; i < 20 && sc.Scan(); i++ {
		var l struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if !l.Timestamp.IsZero() {
			return l.Timestamp, true
		}
	}
	return time.Time{}, false
}

// ParseWindow turns a window name into a duration. Unknown names fall back to
// a day, which is the span most questions are asked over.
func ParseWindow(name string) time.Duration {
	switch name {
	case "1h":
		return time.Hour
	case "3h":
		return 3 * time.Hour
	case "1d":
		return 24 * time.Hour
	case "3d":
		return 72 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// isBoilerplateTitle reports whether a derived title is harness text rather
// than a description of the work.
func isBoilerplateTitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	for _, prefix := range []string{
		"Caveat: The messages below",
		"<command-name>",
		"<local-command",
		"This session is being continued",
	} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// repoLabel names a working directory the way a shell prompt would.
//
// The home directory's basename is the username, which says nothing: a session
// run from ~ was labelled "jayden-eppcohen" alongside every other one. Shells
// solve this with ~, and so does this.
func repoLabel(cwd string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if filepath.Clean(cwd) == filepath.Clean(home) {
			return "~"
		}
	}
	return filepath.Base(cwd)
}

// sortFleet ranks rows by how current each session is.
//
// Ordered by how current a session is, which is a question about its last
// activity rather than its first.
//
// Sorting by StartedAt ranked a session by when it opened, so a long-lived
// one that is still running sank below a five-minute session that happened
// to start later — the row you are actually working in could sit halfway
// down a list of things that finished hours ago. Start time is also the
// least stable key here: a split session inherits its parent's first
// timestamp, so three branches of one thread all claim the same start and
// scatter among rows they have nothing to do with.
//
// Live sessions come first as a block, because "still going" outranks any
// amount of recency among things that have stopped. Within each block the
// most recent activity leads.
func sortFleet(sessions []FleetSession) {
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if a.IsRunning != b.IsRunning {
			return a.IsRunning
		}
		if !a.EndedAt.Equal(b.EndedAt) {
			return a.EndedAt.After(b.EndedAt)
		}
		// Same last activity: a tie among live sessions, which all report
		// "now". Longest-running first, so the session with the most behind it
		// leads rather than the order falling to however the files were read.
		return a.StartedAt.Before(b.StartedAt)
	})
}
