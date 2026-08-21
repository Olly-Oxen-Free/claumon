// Package nimbalyst reads the Nimbalyst desktop app's view of its AI sessions.
//
// Nimbalyst runs Claude Code sessions of its own, and knows two things claumon
// cannot work out from a transcript: the workspace the session belongs to and
// the name the user gave it. It records both against the provider's session id
// — the same id claumon keys on — so the two join cleanly, exactly as the
// herdr integration does.
//
// # Why the database and not the API
//
// Nimbalyst exposes an MCP server on a local port with a `navigate_to_session`
// tool, which would be the natural way in. It is not usable from outside the
// app: the bearer token is `crypto.randomBytes(32)` generated at launch, held
// in a module-level variable, and never written anywhere. There is no way for
// another process to obtain it, so the port answers 401 to everyone but
// Nimbalyst's own children. The `nimbalyst://` URL scheme is likewise no help
// — it routes doc, folder, tracker, invite, auth, install and action, and
// nothing for AI sessions.
//
// What is available is the app's SQLite database, opened read-only. That is a
// private surface and the SDK docs say so plainly: "Tables and columns are not
// part of any stable contract [...] Pin to the host version you tested
// against." So this package treats a schema change as absence rather than as
// an error — every query is allowed to fail, and a failure means "no
// enrichment" exactly as a missing herdr does.
//
// # Navigation
//
// Focusing a session means raising the app and selecting it. Raising the window
// is possible from here; selecting the session is not, for the same reason the
// MCP port is closed. So Reveal does the half that works and says so, rather
// than pretending to the other half.
package nimbalyst

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Session is one Nimbalyst AI session, as Nimbalyst sees it.
type Session struct {
	// ID is Nimbalyst's own id for the session, which is what its UI and its
	// MCP tools address it by.
	ID string `json:"id"`
	// ProviderSessionID is the Claude Code session id — the join key. Empty
	// until the session has actually run a turn, because Nimbalyst creates the
	// row when the session is opened and learns the provider's id later.
	ProviderSessionID string `json:"provider_session_id,omitempty"`
	// Workspace is the project directory the session belongs to. Nimbalyst
	// stores the absolute path here despite the column being named
	// workspace_id.
	Workspace string `json:"workspace,omitempty"`
	// Title is the name shown in Nimbalyst's session list.
	Title string `json:"title,omitempty"`
	// Model is Nimbalyst's own label, e.g. "claude-code:haiku". Kept as
	// written rather than mapped onto claumon's model names: it says which
	// alias the user picked, which the transcript does not record.
	Model string `json:"model,omitempty"`
	// Status is Nimbalyst's live view: idle, running, waiting_for_input, or
	// error. "waiting_for_input" is the one claumon cannot infer at all.
	Status string `json:"status,omitempty"`
	// Worktree is the linked worktree's display name, when the session runs in
	// one.
	Worktree string `json:"worktree,omitempty"`
	// LastActivity is when Nimbalyst last saw the session move.
	LastActivity time.Time `json:"last_activity,omitzero"`
	// Archived sessions are kept out of the join by default; the field is
	// carried so a caller can tell why a session is absent.
	Archived bool `json:"archived,omitempty"`
}

// Client reads Nimbalyst's local database. The zero value is usable.
type Client struct {
	// DBPath overrides the database location. Empty means the default under
	// the user's config directory.
	DBPath string
	// Timeout bounds each query. The database is local and these are tiny
	// reads; anything slower means the app is checkpointing a large WAL, and
	// the dashboard must not wait on it.
	Timeout time.Duration
}

// DefaultDBPath is where the Electron app keeps its SQLite database.
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "@nimbalyst", "electron", "sqlite-db", "nimbalyst.sqlite")
}

func (c Client) dbPath() string {
	if c.DBPath != "" {
		return c.DBPath
	}
	return DefaultDBPath()
}

func (c Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 3 * time.Second
}

// Available reports whether Nimbalyst's database can be read at all.
func (c Client) Available() bool {
	p := c.dbPath()
	if p == "" {
		return false
	}
	if _, err := os.Stat(p); err != nil {
		return false
	}
	_, err := c.List()
	return err == nil
}

// open connects read-only.
//
// Read-only is not only good manners: the app holds the database open in WAL
// mode and is writing to it, and a second writer would risk its data for a
// dashboard's benefit. The immutable flag is deliberately NOT used — it would
// let this read a stale snapshot and ignore the WAL, which is where a running
// session's newest state lives.
func (c Client) open() (*sql.DB, error) {
	p := c.dbPath()
	if p == "" {
		return nil, errors.New("nimbalyst: no database path")
	}
	dsn := "file:" + p + "?mode=ro&_pragma=busy_timeout(2000)"
	return sql.Open("sqlite", dsn)
}

// List returns every non-archived Claude Code session Nimbalyst knows about.
//
// An error means Nimbalyst is absent, not running yet, or has changed its
// schema. All three are "no enrichment" to a caller, never a failure: the
// dashboard is complete without this.
func (c Client) List() ([]Session, error) {
	db, err := c.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
	defer cancel()

	// LEFT JOIN so a session without a worktree still comes back. Only
	// claude-code sessions: Nimbalyst drives other providers too, and a Codex
	// session has no claumon transcript to join to.
	const q = `
		SELECT s.id,
		       COALESCE(s.provider_session_id, ''),
		       COALESCE(s.workspace_id, ''),
		       COALESCE(s.title, ''),
		       COALESCE(s.model, ''),
		       COALESCE(s.status, ''),
		       COALESCE(w.display_name, w.name, ''),
		       COALESCE(s.last_activity, ''),
		       s.is_archived
		  FROM ai_sessions s
		  LEFT JOIN worktrees w ON w.id = s.worktree_id
		 WHERE s.provider = 'claude-code'
		   AND s.is_archived = 0`

	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var s Session
		var lastActivity string
		var archived int
		if err := rows.Scan(&s.ID, &s.ProviderSessionID, &s.Workspace, &s.Title,
			&s.Model, &s.Status, &s.Worktree, &lastActivity, &archived); err != nil {
			return nil, err
		}
		s.Archived = archived != 0
		// Nimbalyst writes strftime('%Y-%m-%dT%H:%M:%fZ'), which is RFC3339
		// with milliseconds. A row that cannot be parsed keeps a zero time
		// rather than being dropped: the title and workspace are still useful.
		if t, err := time.Parse(time.RFC3339, lastActivity); err == nil {
			s.LastActivity = t.UTC()
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// BySession indexes sessions by the Claude Code session id they are running,
// which is how claumon's own records are keyed.
//
// Rows with no provider session id are skipped: Nimbalyst creates a row when a
// session is opened and fills the provider's id in once it runs a turn, so an
// empty value means "not started" rather than "unknown". Where two rows claim
// the same provider id the most recently active wins, since a resumed session
// can appear more than once.
func BySession(sessions []Session) map[string]Session {
	m := make(map[string]Session, len(sessions))
	for _, s := range sessions {
		if s.ProviderSessionID == "" {
			continue
		}
		if prev, ok := m[s.ProviderSessionID]; ok && prev.LastActivity.After(s.LastActivity) {
			continue
		}
		m[s.ProviderSessionID] = s
	}
	return m
}

// WorkspaceName is the workspace path's basename, which is what a person calls
// that project.
func (s Session) WorkspaceName() string {
	if s.Workspace == "" {
		return ""
	}
	return filepath.Base(strings.TrimRight(s.Workspace, "/"))
}

// RevealURL is the deep link that raises Nimbalyst.
//
// It cannot select a session, and it cannot open the session's workspace
// either. Nimbalyst's URL scheme routes doc, folder, tracker, invite, auth,
// install and action — and `folder` is a shared-org folder id requiring an
// orgId, not a filesystem path, so it does not address a local workspace.
// Selecting a session is only possible over the MCP port, whose bearer token
// is generated at launch and never persisted, so no other process can hold it.
//
// What is left is the project manager, which is one click from the session
// list. The caller must say that is what the button does rather than implying
// it lands on the session.
func RevealURL(Session) string {
	return "nimbalyst://action/open-project-manager"
}

// revealHandler is the desktop entry registered for the nimbalyst: scheme.
const revealHandler = "nimbalyst-url-handler.desktop"

// Reveal raises the Nimbalyst window.
//
// It takes no argument on purpose. There is exactly one URL worth opening —
// see RevealURL — and building it here rather than accepting one from a caller
// keeps this from becoming a general-purpose launcher for arbitrary schemes.
//
// gtk-launch, not xdg-open. `xdg-mime query default x-scheme-handler/nimbalyst`
// names the handler correctly, but xdg-open on this desktop ignores it and
// walks its browser list instead, failing with exit 3 after finding no
// browser. gtk-launch invokes the registered entry directly, which is what
// hands the URL to the running instance rather than starting a second one.
// xdg-open remains the fallback for a desktop where gtk-launch is absent.
func Reveal() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := RevealURL(Session{})
	if _, err := exec.LookPath("gtk-launch"); err == nil {
		if err := exec.CommandContext(ctx, "gtk-launch", revealHandler, url).Run(); err == nil {
			return nil
		}
	}
	return exec.CommandContext(ctx, "xdg-open", url).Run()
}
