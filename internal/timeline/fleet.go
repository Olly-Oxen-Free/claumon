package timeline

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fabioconcina/claumon/internal/herdr"
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
	Repo      string `json:"repo,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	GitBranch string `json:"git_branch,omitempty"`
	Model     string `json:"model,omitempty"`
	Title     string `json:"title,omitempty"`

	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	CostUSD   float64   `json:"cost_usd"`
	Messages  int       `json:"messages"`
	IsRunning bool      `json:"is_running"`

	Agents []FleetAgent `json:"agents,omitempty"`

	// Herdr is the pane this session is running in, when the workspace
	// manager knows about it. Absent when herdr is not running.
	Herdr *HerdrRef `json:"herdr,omitempty"`
}

// HerdrRef locates a session in the terminal workspace manager, so the
// dashboard can name the task the way the user named it and put the pane in
// front of them.
type HerdrRef struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Title is the tab's task name.
	Title string `json:"title,omitempty"`
	// Status is herdr's live view: working or idle. More reliable than
	// inferring from file mtimes, because herdr watches the process.
	Status  string `json:"status,omitempty"`
	Focused bool   `json:"focused,omitempty"`
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
func BuildFleet(claudeDir string, from, to time.Time, scanLimit int) (*Fleet, error) {
	// One call, reused for every session: herdr is asked once per request
	// rather than once per row. Failure is silent by design — the fleet is
	// complete without it.
	panes := map[string]herdr.Agent{}
	if agents, err := (herdr.Client{}).List(); err == nil {
		panes = herdr.BySession(agents)
	}

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
		if path != "" {
			if c, b, ok := sessionContext(path); ok {
				if c != "" {
					cwd = c
				}
				branch = b
			}
		}

		fs := FleetSession{
			SessionID: s.ID,
			Cwd:       cwd,
			GitBranch: branch,
			Model:     s.Model,
			Title:     s.Title,
			StartedAt: start,
			EndedAt:   end,
			CostUSD:   s.EstimatedCostUSD,
			Messages:  s.MessageCount,
			IsRunning: s.IsRunning,
		}
		if cwd != "" {
			fs.Repo = filepath.Base(cwd)
		} else if s.Project != "" {
			fs.Repo = filepath.Base(s.Project)
		}
		if path != "" {
			fs.Agents = agentSpans(sessionDir(path, s.ID))
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
			// herdr watches the process, so its view of "working" beats an
			// inference drawn from transcript mtimes.
			if h.Status == "working" {
				fs.IsRunning = true
			}
			if fs.Title == "" {
				fs.Title = h.Title
			}
		}
		out.Sessions = append(out.Sessions, fs)
	}

	// Newest first: the thing you just ran is the thing you are looking for.
	sort.SliceStable(out.Sessions, func(i, j int) bool {
		return out.Sessions[i].StartedAt.After(out.Sessions[j].StartedAt)
	})
	return out, nil
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
