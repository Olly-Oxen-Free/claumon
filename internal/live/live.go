// Package live reports which Claude Code sessions are running right now, and
// what each one is doing.
//
// claumon already discovers sessions by parsing transcript files, which tells
// it what a session has *done*. It cannot tell whether the session is mid-turn,
// waiting on a permission answer, or finished, because none of that is written
// to the transcript as it happens.
//
// Claude Code hooks can be. This package reads the per-session state files
// under ~/.claude/statusbar/state.d/, written by a hook on every lifecycle
// event, and turns them into a live status per session. The layout originates
// with m1ckc3s/claude-status-bar and is shared with the NirvanaOS bar chip, so
// one hook install feeds both.
//
// The directory is optional. With no hooks installed it does not exist, Status
// returns nothing, and every other part of claumon is unaffected.
package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Status is a session's current activity.
type Status string

const (
	// StatusWorking: a model turn or tool call is in flight.
	StatusWorking Status = "working"
	// StatusWaiting: blocked on the user answering a permission prompt.
	StatusWaiting Status = "waiting"
	// StatusIdle: open, with no turn in flight.
	StatusIdle Status = "idle"
	// StatusDone: the last turn completed.
	StatusDone Status = "done"
)

// Session is one live Claude Code session.
type Session struct {
	SessionID string `json:"session_id"`
	Status    Status `json:"status"`
	// Label is the activity text, e.g. "Editing". Empty when resting.
	Label string `json:"label,omitempty"`
	// Tool is the raw tool name when one is running.
	Tool string `json:"tool,omitempty"`
	// Project is the basename of the session's working directory.
	Project string `json:"project,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	// PID of the session's `claude` process.
	PID int `json:"pid"`
	// TurnSeconds is how long the current turn has been running, 0 when
	// resting.
	TurnSeconds int64 `json:"turn_seconds"`
	// UpdatedAt is when the hook last wrote this session's file.
	UpdatedAt time.Time `json:"updated_at"`
}

// stateFile is the on-disk shape written by the hook.
type stateFile struct {
	State     string `json:"state"`
	Label     string `json:"label"`
	Tool      string `json:"tool"`
	Project   string `json:"project"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	Started   bool   `json:"started"`
	StartedAt int64  `json:"started_at"`
	TS        int64  `json:"ts"`
}

// StateDir returns the directory the hooks write to.
func StateDir(claudeDir string) string {
	return filepath.Join(claudeDir, "statusbar", "state.d")
}

// A session file older than this is stale regardless of whether its pid still
// resolves, guarding against a pid reused by an unrelated process.
const staleAfter = 6 * time.Hour

func toStatus(raw string) Status {
	switch raw {
	case "thinking", "tool":
		return StatusWorking
	case "permission":
		return StatusWaiting
	case "done":
		return StatusDone
	default:
		return StatusIdle
	}
}

// rank orders sessions for display: the ones needing attention first, then the
// ones working, then the resting ones.
func rank(s Status) int {
	switch s {
	case StatusWaiting:
		return 0
	case StatusWorking:
		return 1
	case StatusIdle:
		return 2
	default:
		return 3
	}
}

// Reader reads live session state from a claude directory.
type Reader struct {
	dir string
	// alive reports whether a pid is still running. Injectable for tests.
	alive func(pid int) bool
	now   func() time.Time
}

func NewReader(claudeDir string) *Reader {
	return &Reader{dir: StateDir(claudeDir), alive: pidAlive, now: time.Now}
}

// Sessions returns every live session, ranked. A session whose process is gone
// or whose file has gone stale is dropped and its file removed: nothing else
// prunes this directory, and a crashed session leaves its file behind.
//
// Sessions that were opened but never used are excluded — they would pad the
// count with conversations the user merely clicked into.
func (r *Reader) Sessions() []Session {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	now := r.now()
	out := make([]Session, 0, len(entries))

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var sf stateFile
		if err := json.Unmarshal(data, &sf); err != nil {
			continue
		}
		updated := time.Unix(sf.TS, 0)
		if sf.PID <= 0 || !r.alive(sf.PID) || now.Sub(updated) > staleAfter {
			_ = os.Remove(path)
			continue
		}
		if !sf.Started {
			continue
		}
		var turn int64
		if sf.StartedAt > 0 {
			turn = now.Unix() - sf.StartedAt
			if turn < 0 {
				turn = 0
			}
		}
		out = append(out, Session{
			SessionID:   sf.SessionID,
			Status:      toStatus(sf.State),
			Label:       sf.Label,
			Tool:        sf.Tool,
			Project:     sf.Project,
			Cwd:         sf.Cwd,
			PID:         sf.PID,
			TurnSeconds: turn,
			UpdatedAt:   updated,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i].Status), rank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// ByID indexes sessions for joining onto claumon's transcript-derived session
// list, which keys on the same session id.
func ByID(sessions []Session) map[string]Session {
	m := make(map[string]Session, len(sessions))
	for _, s := range sessions {
		if s.SessionID != "" {
			m[s.SessionID] = s
		}
	}
	return m
}
