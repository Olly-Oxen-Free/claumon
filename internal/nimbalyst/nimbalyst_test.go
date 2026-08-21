package nimbalyst

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// fixture writes a database shaped like Nimbalyst's, with only the columns
// this package reads. If Nimbalyst adds columns the queries keep working; if it
// renames one of these, List fails and the caller degrades to no enrichment —
// which is the documented contract, since the schema is explicitly not stable.
func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nimbalyst.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE worktrees (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL,
			path TEXT NOT NULL, branch TEXT NOT NULL, display_name TEXT,
			is_archived INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE ai_sessions (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL DEFAULT 'default',
			provider TEXT NOT NULL, model TEXT, title TEXT NOT NULL DEFAULT 'New conversation',
			provider_session_id TEXT, status TEXT DEFAULT 'idle',
			last_activity TEXT, worktree_id TEXT, is_archived INTEGER NOT NULL DEFAULT 0);
	`); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListJoinsWorkspaceTitleAndWorktree(t *testing.T) {
	path := fixture(t)
	db, _ := sql.Open("sqlite", "file:"+path)
	db.Exec(`INSERT INTO worktrees (id,workspace_id,name,path,branch,display_name)
	         VALUES ('w1','/home/me/proj','feature-x','/home/me/wt','feat','Feature X')`)
	db.Exec(`INSERT INTO ai_sessions (id,workspace_id,provider,model,title,provider_session_id,status,last_activity,worktree_id)
	         VALUES ('n1','/home/me/proj','claude-code','claude-code:haiku','tiny test','abc-123','running','2026-08-19T22:27:06.115Z','w1')`)
	db.Close()

	got, err := (Client{DBPath: path}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.ProviderSessionID != "abc-123" || s.Title != "tiny test" ||
		s.Workspace != "/home/me/proj" || s.Worktree != "Feature X" ||
		s.Status != "running" || s.Model != "claude-code:haiku" {
		t.Errorf("session = %+v", s)
	}
	if s.WorkspaceName() != "proj" {
		t.Errorf("WorkspaceName() = %q, want proj", s.WorkspaceName())
	}
	want := time.Date(2026, 8, 19, 22, 27, 6, 115000000, time.UTC)
	if !s.LastActivity.Equal(want) {
		t.Errorf("LastActivity = %s, want %s", s.LastActivity, want)
	}
}

func TestListSkipsOtherProvidersAndArchived(t *testing.T) {
	path := fixture(t)
	db, _ := sql.Open("sqlite", "file:"+path)
	// A Codex session has no claumon transcript to join to.
	db.Exec(`INSERT INTO ai_sessions (id,provider,provider_session_id) VALUES ('n1','codex','x')`)
	db.Exec(`INSERT INTO ai_sessions (id,provider,provider_session_id,is_archived) VALUES ('n2','claude-code','y',1)`)
	db.Exec(`INSERT INTO ai_sessions (id,provider,provider_session_id) VALUES ('n3','claude-code','z')`)
	db.Close()

	got, err := (Client{DBPath: path}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ProviderSessionID != "z" {
		t.Fatalf("got %+v, want only the live claude-code session", got)
	}
}

func TestSessionWithoutAWorktreeStillLists(t *testing.T) {
	path := fixture(t)
	db, _ := sql.Open("sqlite", "file:"+path)
	db.Exec(`INSERT INTO ai_sessions (id,workspace_id,provider,provider_session_id)
	         VALUES ('n1','/home/me/proj','claude-code','abc')`)
	db.Close()

	got, err := (Client{DBPath: path}).List()
	if err != nil {
		t.Fatal(err)
	}
	// The LEFT JOIN must not drop it.
	if len(got) != 1 || got[0].Worktree != "" {
		t.Fatalf("got %+v, want one session with no worktree", got)
	}
}

func TestBySessionSkipsUnstartedAndPrefersRecent(t *testing.T) {
	// Nimbalyst creates the row when a session is opened and learns the
	// provider's id once it runs a turn, so an empty id is "not started".
	sessions := []Session{
		{ID: "a", ProviderSessionID: ""},
		{ID: "b", ProviderSessionID: "dup", Title: "older", LastActivity: time.Unix(100, 0)},
		{ID: "c", ProviderSessionID: "dup", Title: "newer", LastActivity: time.Unix(200, 0)},
	}
	m := BySession(sessions)
	if len(m) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(m), m)
	}
	if m["dup"].Title != "newer" {
		t.Errorf("kept %q, want the most recently active", m["dup"].Title)
	}
}

func TestMissingDatabaseIsNotAvailable(t *testing.T) {
	c := Client{DBPath: filepath.Join(t.TempDir(), "absent.sqlite")}
	if c.Available() {
		t.Error("a missing database must report unavailable, not error out")
	}
	if _, err := c.List(); err == nil {
		t.Error("List on a missing database should error so the caller can skip enrichment")
	}
}

func TestSchemaChangeDegradesToAnError(t *testing.T) {
	// The SDK docs say the schema is explicitly not a stable contract, so a
	// renamed table must surface as an error the caller treats as absence —
	// never as a panic or as silently empty data.
	path := filepath.Join(t.TempDir(), "nimbalyst.sqlite")
	db, _ := sql.Open("sqlite", "file:"+path)
	db.Exec(`CREATE TABLE something_else (id TEXT)`)
	db.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := (Client{DBPath: path}).List(); err == nil {
		t.Error("a missing ai_sessions table must be an error")
	}
}

func TestRevealURLIsTheProjectManager(t *testing.T) {
	// Pinned deliberately: nimbalyst://folder/ looks like it would open a
	// workspace but takes a shared-org folder id plus an orgId, and no route
	// addresses an AI session. If a future version adds one, this test is the
	// reminder to use it.
	if got := RevealURL(Session{Workspace: "/home/me/proj"}); got != "nimbalyst://action/open-project-manager" {
		t.Errorf("RevealURL = %q", got)
	}
}
