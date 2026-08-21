package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeState(t *testing.T, dir string, sf stateFile) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}
	name := sf.SessionID
	if name == "" {
		name = "unnamed"
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// reader over a temp claude dir with every pid alive and time frozen.
func testReader(t *testing.T, now time.Time) (*Reader, string) {
	t.Helper()
	claudeDir := t.TempDir()
	r := NewReader(claudeDir)
	r.alive = func(int) bool { return true }
	r.now = func() time.Time { return now }
	return r, StateDir(claudeDir)
}

func TestMissingDirectoryReturnsNothing(t *testing.T) {
	r := NewReader(t.TempDir())
	if got := r.Sessions(); len(got) != 0 {
		t.Fatalf("want no sessions without hooks installed, got %d", len(got))
	}
}

func TestStatesMapToStatuses(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r, dir := testReader(t, now)
	for _, tc := range []struct {
		state string
		want  Status
	}{
		{"thinking", StatusWorking},
		{"tool", StatusWorking},
		{"permission", StatusWaiting},
		{"done", StatusDone},
		{"idle", StatusIdle},
	} {
		writeState(t, dir, stateFile{
			SessionID: tc.state, State: tc.state, PID: 1, Started: true, TS: now.Unix(),
		})
	}
	got := ByID(r.Sessions())
	if len(got) != 5 {
		t.Fatalf("want 5 sessions, got %d", len(got))
	}
	for _, tc := range []struct {
		id   string
		want Status
	}{
		{"thinking", StatusWorking},
		{"tool", StatusWorking},
		{"permission", StatusWaiting},
		{"done", StatusDone},
		{"idle", StatusIdle},
	} {
		if got[tc.id].Status != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.id, got[tc.id].Status, tc.want)
		}
	}
}

func TestWaitingSessionsSortFirst(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r, dir := testReader(t, now)
	writeState(t, dir, stateFile{SessionID: "a", State: "idle", PID: 1, Started: true, TS: now.Unix()})
	writeState(t, dir, stateFile{SessionID: "b", State: "tool", PID: 1, Started: true, TS: now.Unix()})
	writeState(t, dir, stateFile{SessionID: "c", State: "permission", PID: 1, Started: true, TS: now.Unix()})

	got := r.Sessions()
	if got[0].SessionID != "c" {
		t.Fatalf("a session awaiting permission must lead, got %q", got[0].SessionID)
	}
	if got[1].SessionID != "b" {
		t.Fatalf("working should outrank idle, got %q", got[1].SessionID)
	}
}

func TestUnstartedSessionsAreHidden(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r, dir := testReader(t, now)
	writeState(t, dir, stateFile{SessionID: "opened", State: "idle", PID: 1, Started: false, TS: now.Unix()})
	if got := r.Sessions(); len(got) != 0 {
		t.Fatalf("a merely-opened conversation must not count, got %d", len(got))
	}
}

func TestDeadSessionsArePrunedFromDisk(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	claudeDir := t.TempDir()
	r := NewReader(claudeDir)
	r.alive = func(int) bool { return false }
	r.now = func() time.Time { return now }
	dir := StateDir(claudeDir)
	writeState(t, dir, stateFile{SessionID: "ghost", State: "tool", PID: 4242, Started: true, TS: now.Unix()})

	if got := r.Sessions(); len(got) != 0 {
		t.Fatalf("a session whose process is gone must not be listed, got %d", len(got))
	}
	if _, err := os.Stat(filepath.Join(dir, "ghost.json")); !os.IsNotExist(err) {
		t.Fatal("the dead session's file should have been removed")
	}
}

func TestStaleFilesArePrunedEvenWhenThePidResolves(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r, dir := testReader(t, now)
	old := now.Add(-staleAfter - time.Minute).Unix()
	writeState(t, dir, stateFile{SessionID: "old", State: "tool", PID: 1, Started: true, TS: old})
	if got := r.Sessions(); len(got) != 0 {
		t.Fatalf("stale file must be dropped, got %d", len(got))
	}
}

func TestTurnSecondsCountFromTheTurnStart(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r, dir := testReader(t, now)
	writeState(t, dir, stateFile{
		SessionID: "a", State: "tool", PID: 1, Started: true,
		StartedAt: now.Unix() - 64, TS: now.Unix(),
	})
	got := r.Sessions()
	if got[0].TurnSeconds != 64 {
		t.Fatalf("turn_seconds = %d, want 64", got[0].TurnSeconds)
	}
}

func TestRestingSessionHasNoTurnClock(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r, dir := testReader(t, now)
	writeState(t, dir, stateFile{
		SessionID: "a", State: "done", PID: 1, Started: true, StartedAt: 0, TS: now.Unix(),
	})
	if got := r.Sessions(); got[0].TurnSeconds != 0 {
		t.Fatalf("turn_seconds = %d, want 0 for a resting session", got[0].TurnSeconds)
	}
}

func TestMalformedFilesAreSkippedNotFatal(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r, dir := testReader(t, now)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeState(t, dir, stateFile{SessionID: "good", State: "tool", PID: 1, Started: true, TS: now.Unix()})

	got := r.Sessions()
	if len(got) != 1 || got[0].SessionID != "good" {
		t.Fatalf("one bad file must not hide the good ones: %+v", got)
	}
}
