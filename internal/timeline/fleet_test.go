package timeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fleetFixture writes a session transcript whose lines carry timestamps, so
// the summary parser derives a real start and end.
func fleetFixture(t *testing.T, sessionID string, at0 time.Time, mins int) string {
	t.Helper()
	claudeDir := t.TempDir()
	projDir := filepath.Join(claudeDir, "projects", "-home-me-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for i := 0; i <= mins; i++ {
		l := map[string]any{
			"type": "assistant", "timestamp": at0.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"cwd": "/home/me/proj", "gitBranch": "main",
			"message": map[string]any{"role": "assistant", "model": "claude-sonnet-4-6",
				"content": []any{map[string]any{"type": "text", "text": "hi"}},
				"usage":   map[string]any{"input_tokens": 10, "output_tokens": 5}},
		}
		b, _ := json.Marshal(l)
		body = append(append(body, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(projDir, sessionID+".jsonl"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return claudeDir
}

func TestFleetIncludesASessionInsideTheWindow(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	dir := fleetFixture(t, "sess-a", start, 10)

	f, err := BuildFleet(dir, time.Now().Add(-24*time.Hour), time.Now(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(f.Sessions))
	}
	s := f.Sessions[0]
	if s.Repo != "proj" {
		t.Fatalf("repo = %q, want the working tree's basename", s.Repo)
	}
	if s.GitBranch != "main" {
		t.Fatalf("branch = %q", s.GitBranch)
	}
	if !s.EndedAt.After(s.StartedAt) {
		t.Fatalf("span not derived: %v..%v", s.StartedAt, s.EndedAt)
	}
}

func TestFleetExcludesASessionOutsideTheWindow(t *testing.T) {
	dir := fleetFixture(t, "sess-old", time.Now().Add(-48*time.Hour), 5)
	f, err := BuildFleet(dir, time.Now().Add(-1*time.Hour), time.Now(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sessions) != 0 {
		t.Fatalf("a session that ended before the window must not appear: %+v", f.Sessions)
	}
}

func TestFleetIncludesASessionStraddlingTheWindowStart(t *testing.T) {
	// Started three hours ago, still going: overlap, not containment, is the
	// test — otherwise a long-running session vanishes from its own window.
	dir := fleetFixture(t, "sess-long", time.Now().Add(-3*time.Hour), 170)
	f, err := BuildFleet(dir, time.Now().Add(-1*time.Hour), time.Now(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sessions) != 1 {
		t.Fatalf("a session straddling the window start must appear: %+v", f.Sessions)
	}
}

func TestFleetReportsAgentsAsSpans(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	dir := fleetFixture(t, "sess-a", start, 10)

	agentDir := filepath.Join(dir, "projects", "-home-me-proj", "sess-a", "subagents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := map[string]any{
		"type": "user", "timestamp": start.Add(time.Minute).Format(time.RFC3339),
		"message": map[string]any{"role": "user", "content": "go"},
	}
	b, _ := json.Marshal(line)
	if err := os.WriteFile(filepath.Join(agentDir, "agent-abc123.jsonl"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(agentMeta{AgentType: "Explore", Description: "look around", SpawnDepth: 1})
	if err := os.WriteFile(filepath.Join(agentDir, "agent-abc123.meta.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := BuildFleet(dir, time.Now().Add(-24*time.Hour), time.Now(), 50)
	if err != nil {
		t.Fatal(err)
	}
	agents := f.Sessions[0].Agents
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	a := agents[0]
	if a.AgentType != "Explore" || a.Description != "look around" || a.SpawnDepth != 1 {
		t.Fatalf("meta not carried: %+v", a)
	}
	if a.StartedAt.IsZero() {
		t.Fatal("agent start must come from its first transcript line")
	}
	if a.EndedAt.Before(a.StartedAt) {
		t.Fatalf("agent end before start: %v..%v", a.StartedAt, a.EndedAt)
	}
}

func TestFleetSortsNewestFirst(t *testing.T) {
	dir := fleetFixture(t, "older", time.Now().Add(-5*time.Hour), 5)
	// A second session in the same project directory, more recent.
	projDir := filepath.Join(dir, "projects", "-home-me-proj")
	newer := time.Now().Add(-1 * time.Hour)
	var body []byte
	for i := 0; i < 3; i++ {
		l := map[string]any{
			"type": "assistant", "timestamp": newer.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"cwd":     "/home/me/proj",
			"message": map[string]any{"role": "assistant", "model": "m", "content": []any{}},
		}
		b, _ := json.Marshal(l)
		body = append(append(body, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(projDir, "newer.jsonl"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := BuildFleet(dir, time.Now().Add(-24*time.Hour), time.Now(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(f.Sessions))
	}
	if f.Sessions[0].SessionID != "newer" {
		t.Fatalf("newest must lead, got %q", f.Sessions[0].SessionID)
	}
}

func TestParseWindowCoversEveryOfferedRange(t *testing.T) {
	for name, want := range map[string]time.Duration{
		"1h": time.Hour,
		"3h": 3 * time.Hour,
		"1d": 24 * time.Hour,
		"3d": 72 * time.Hour,
		"1w": 7 * 24 * time.Hour,
	} {
		if got := ParseWindow(name); got != want {
			t.Errorf("ParseWindow(%q) = %v, want %v", name, got, want)
		}
	}
	// An unknown name must not produce a zero-length window, which would
	// render an empty chart with no explanation.
	if got := ParseWindow("nonsense"); got != 24*time.Hour {
		t.Fatalf("fallback = %v, want a day", got)
	}
}

func TestFleetOnAnEmptyClaudeDirIsEmptyNotAnError(t *testing.T) {
	f, err := BuildFleet(t.TempDir(), time.Now().Add(-time.Hour), time.Now(), 50)
	if err != nil {
		t.Fatalf("empty dir should not error: %v", err)
	}
	if len(f.Sessions) != 0 {
		t.Fatalf("want no sessions, got %d", len(f.Sessions))
	}
}
