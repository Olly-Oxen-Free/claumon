package herdr

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeHerdr writes a stub executable that prints canned output, so the client
// is tested without a running workspace manager.
func fakeHerdr(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\ncat <<'JSON'\n" + stdout + "\nJSON\nexit " +
		map[bool]string{true: "0", false: "1"}[exitCode == 0] + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const sample = `{"id":"cli:agent:list","result":{"agents":[
 {"agent":"claude","agent_session":{"value":"sess-aaa"},"agent_status":"working",
  "cwd":"/home/me/work","focused":true,"pane_id":"w3:p2","tab_id":"w3:t2",
  "workspace_id":"w3","terminal_title":"✳ Audit things","terminal_title_stripped":"Audit things"},
 {"agent":"codex","agent_session":{"value":"sess-bbb"},"agent_status":"idle",
  "cwd":"/home/me/other","focused":false,"pane_id":"w6:p1","tab_id":"w6:t1",
  "workspace_id":"w6","terminal_title":"Other task","terminal_title_stripped":""}
]}}`

func TestListParsesAgents(t *testing.T) {
	c := Client{Bin: fakeHerdr(t, sample, 0)}
	got, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 agents, got %d", len(got))
	}
	a := got[0]
	if a.PaneID != "w3:p2" || a.WorkspaceID != "w3" || a.Status != "working" {
		t.Fatalf("agent fields wrong: %+v", a)
	}
	if a.SessionID != "sess-aaa" {
		t.Fatalf("session id = %q — this is the join key", a.SessionID)
	}
	if !a.Focused {
		t.Fatal("focused flag not carried")
	}
}

func TestTitlePrefersTheStrippedForm(t *testing.T) {
	c := Client{Bin: fakeHerdr(t, sample, 0)}
	got, _ := c.List()
	// The raw title carries a spinner glyph that changes every frame; the
	// stripped form is the stable name.
	if got[0].Title != "Audit things" {
		t.Fatalf("title = %q, want the stripped form", got[0].Title)
	}
	// ...but falls back when herdr does not supply one.
	if got[1].Title != "Other task" {
		t.Fatalf("fallback title = %q", got[1].Title)
	}
}

func TestBySessionIndexesOnTheJoinKey(t *testing.T) {
	c := Client{Bin: fakeHerdr(t, sample, 0)}
	got, _ := c.List()
	idx := BySession(got)
	if idx["sess-aaa"].PaneID != "w3:p2" {
		t.Fatalf("index wrong: %+v", idx)
	}
	if _, ok := idx[""]; ok {
		t.Fatal("an agent with no session id must not create an empty-key entry")
	}
}

func TestMissingHerdrIsNotAvailable(t *testing.T) {
	c := Client{Bin: filepath.Join(t.TempDir(), "definitely-not-here")}
	if c.Available() {
		t.Fatal("a missing binary must report unavailable, not panic")
	}
	if _, err := c.List(); err == nil {
		t.Fatal("listing without herdr must error so callers can skip enrichment")
	}
}

func TestGarbageOutputIsAnErrorNotAPanic(t *testing.T) {
	c := Client{Bin: fakeHerdr(t, "not json at all", 0)}
	if _, err := c.List(); err == nil {
		t.Fatal("unparseable output must be an error")
	}
}

func TestPaneIDsAreConstrainedBeforeReachingASubprocess(t *testing.T) {
	for _, ok := range []string{"w3:p2", "w12:p10", "W1:P1"} {
		if !ValidPaneID(ok) {
			t.Errorf("ValidPaneID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"", "w3", "w3 p2", "w3;rm -rf /", "$(whoami)", "w3:p2 && echo hi",
		"../../etc/passwd", "w3:p2\n", string(make([]byte, 64)),
	} {
		if ValidPaneID(bad) {
			t.Errorf("ValidPaneID(%q) = true, want false", bad)
		}
	}
}

func TestSpinnerGlyphsAreStrippedFromTitles(t *testing.T) {
	// herdr's "stripped" title still carries Claude Code's animated status
	// glyph, which changes every frame; a label built from it would flicker.
	for in, want := range map[string]string{
		"✳ Audit and optimize":     "Audit and optimize",
		"◑ Install claumon":        "Install claumon",
		"◐  Fix the button":        "Fix the button",
		"Plain title":              "Plain title",
		"  ✻ ✽ Nested glyphs":      "Nested glyphs",
		"/home/me/path as a title": "/home/me/path as a title",
		"\"quoted start\"":         "\"quoted start\"",
		"":                         "",
		"✳":                        "",
	} {
		if got := stripSpinner(in); got != want {
			t.Errorf("stripSpinner(%q) = %q, want %q", in, got, want)
		}
	}
}
