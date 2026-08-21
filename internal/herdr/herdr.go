// Package herdr reads the terminal workspace manager these sessions run in.
//
// herdr knows something claumon cannot work out on its own: which pane a
// Claude session is running in, what the user named that task, and whether the
// agent is working or idle right now. It keys all of that by the same session
// id claumon uses, so the two join cleanly.
//
// The join is what makes the dashboard actionable rather than merely
// informative: a row stops being "some session in Documents/Work" and becomes
// "the pane in workspace 3 titled 'Audit AI tool usage', still working" — and
// one click can put it in front of you.
//
// Everything degrades to nothing when herdr is absent. It is not a dependency;
// it is an enrichment.
package herdr

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

// Agent is one agent session as herdr sees it.
type Agent struct {
	// PaneID addresses the pane, e.g. "w3:p2". This is what focus takes.
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	// Agent is the tool running there: claude, codex, and so on.
	Agent string `json:"agent"`
	// Status is herdr's live view: "working" or "idle". claumon infers the
	// same thing from process state and file mtimes; herdr simply knows.
	Status string `json:"agent_status"`
	// Title is the task name shown on the tab, with spinner glyphs stripped.
	Title   string `json:"title"`
	Cwd     string `json:"cwd"`
	Focused bool   `json:"focused"`
	// SessionID is the agent's own session id — the join key.
	SessionID string `json:"session_id"`
}

// wire mirrors `herdr agent list` output.
type wire struct {
	Result struct {
		Agents []struct {
			PaneID       string `json:"pane_id"`
			TabID        string `json:"tab_id"`
			WorkspaceID  string `json:"workspace_id"`
			Agent        string `json:"agent"`
			AgentStatus  string `json:"agent_status"`
			Cwd          string `json:"cwd"`
			Focused      bool   `json:"focused"`
			TitleStrip   string `json:"terminal_title_stripped"`
			Title        string `json:"terminal_title"`
			AgentSession struct {
				Value string `json:"value"`
			} `json:"agent_session"`
		} `json:"agents"`
	} `json:"result"`
}

// Client talks to herdr. The zero value is usable.
type Client struct {
	// Bin is the herdr executable; defaults to looking it up on PATH.
	Bin string
	// Timeout bounds each call. herdr answers over a local socket in
	// milliseconds; anything slower means it is wedged, and the dashboard
	// must not wait on it.
	Timeout time.Duration
}

func (c Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "herdr"
}

func (c Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 3 * time.Second
}

// Available reports whether herdr can be reached at all.
func (c Client) Available() bool {
	if _, err := exec.LookPath(c.bin()); err != nil {
		return false
	}
	_, err := c.List()
	return err == nil
}

// List returns every agent herdr is tracking.
//
// An error means herdr is absent or not answering, which callers treat as "no
// enrichment" rather than a failure: the dashboard works without it.
func (c Client) List() ([]Agent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
	defer cancel()

	out, err := exec.CommandContext(ctx, c.bin(), "agent", "list").Output()
	if err != nil {
		return nil, err
	}
	var w wire
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, err
	}

	agents := make([]Agent, 0, len(w.Result.Agents))
	for _, a := range w.Result.Agents {
		title := stripSpinner(a.TitleStrip)
		if title == "" {
			title = stripSpinner(a.Title)
		}
		agents = append(agents, Agent{
			PaneID:      a.PaneID,
			TabID:       a.TabID,
			WorkspaceID: a.WorkspaceID,
			Agent:       a.Agent,
			Status:      a.AgentStatus,
			Title:       title,
			Cwd:         a.Cwd,
			Focused:     a.Focused,
			SessionID:   a.AgentSession.Value,
		})
	}
	return agents, nil
}

// stripSpinner removes the animated status glyph Claude Code prefixes to a tab
// title. herdr's "stripped" title still carries it, and it changes every frame,
// so a title used as a label would flicker between renders.
func stripSpinner(s string) string {
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		r := runes[i]
		// Keep anything that could begin a real title: letters, digits, and
		// the punctuation a sentence legitimately starts with.
		if unicode.IsLetter(r) || unicode.IsDigit(r) ||
			r == '"' || r == '\'' || r == '(' || r == '[' || r == '/' || r == '.' {
			break
		}
		i++
	}
	return strings.TrimSpace(string(runes[i:]))
}

// BySession indexes agents by the session id they are running, which is how
// claumon's own records are keyed.
func BySession(agents []Agent) map[string]Agent {
	m := make(map[string]Agent, len(agents))
	for _, a := range agents {
		if a.SessionID != "" {
			m[a.SessionID] = a
		}
	}
	return m
}

// Focus brings a pane to the front.
//
// This only ever moves the user's own attention to a pane they already own. It
// deliberately does not wrap `herdr agent prompt` or `send-keys`: those put
// words into another agent's input and spend its context, which is not a
// dashboard's decision to make.
func (c Client) Focus(paneID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
	defer cancel()
	return exec.CommandContext(ctx, c.bin(), "agent", "focus", paneID).Run()
}

// ValidPaneID reports whether s looks like a pane address.
//
// Pane ids arrive from HTTP requests and are handed to a subprocess argument,
// so they are constrained to the shape herdr actually emits rather than passed
// through on trust.
func ValidPaneID(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	colon := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r == ':':
			colon = true
		default:
			return false
		}
	}
	return colon
}
