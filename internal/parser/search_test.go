package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSession(t *testing.T, claudeDir, project, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const userMsg = `{"type":"user","timestamp":"2026-08-18T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":%q}]}}`

func TestSearchSessionsFindsMatch(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "-home-u-proj", "sess-a",
		fmtLine(userMsg, "the GSK_RENDERER re-exec is required on this driver"))
	writeSession(t, dir, "-home-u-other", "sess-b",
		fmtLine(userMsg, "completely unrelated content"))

	res, err := SearchSessions(dir, "gsk_renderer", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2", res.FilesScanned)
	}
	if res.FilesMatched != 1 {
		t.Errorf("FilesMatched = %d, want 1 (prefilter should reject the other)", res.FilesMatched)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(res.Hits))
	}
	h := res.Hits[0]
	if h.SessionID != "sess-a" {
		t.Errorf("SessionID = %q, want sess-a", h.SessionID)
	}
	if !strings.Contains(strings.ToLower(h.Snippet), "gsk_renderer") {
		t.Errorf("snippet missing the match: %q", h.Snippet)
	}
	if h.Field != "text" {
		t.Errorf("Field = %q, want text", h.Field)
	}
}

func TestSearchSessionsEmptyQuery(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "-home-u-proj", "s", fmtLine(userMsg, "anything"))
	res, err := SearchSessions(dir, "   ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 || res.FilesScanned != 0 {
		t.Errorf("empty query must short-circuit, got %d hits / %d scanned", len(res.Hits), res.FilesScanned)
	}
}

func TestSearchSessionsRespectsLimit(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmtLine(userMsg, "needle here"))
	}
	writeSession(t, dir, "-home-u-proj", "many", lines...)

	res, err := SearchSessions(dir, "needle", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 3 {
		t.Errorf("hits = %d, want 3", len(res.Hits))
	}
	if !res.Truncated {
		t.Error("Truncated should be true when the limit is hit")
	}
}

func TestSnippetCollapsesWhitespace(t *testing.T) {
	body := "alpha\n\n   beta   needle\tgamma\n\ndelta"
	got := snippet(body, strings.Index(body, "needle"), len("needle"))
	if strings.ContainsAny(got, "\n\t") {
		t.Errorf("snippet must be one line, got %q", got)
	}
	if !strings.Contains(got, "needle") {
		t.Errorf("snippet lost the match: %q", got)
	}
}

func fmtLine(tmpl, text string) string {
	return fmt.Sprintf(tmpl, text)
}
