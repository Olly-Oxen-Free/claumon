package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractWikiLinks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"plain wikilink", "see [[lyra-foundation]] for detail", []string{"lyra-foundation.md"}},
		{"aliased", "see [[lyra-foundation|the foundation]]", []string{"lyra-foundation.md"}},
		{"heading anchor", "see [[lyra-foundation#Setup]]", []string{"lyra-foundation.md"}},
		{"anchor and alias", "[[page#Sec|label]]", []string{"page.md"}},
		{"already has .md", "[[notes.md]]", []string{"notes.md"}},
		{"empty target ignored", "[[]] and [[ ]]", nil},
		{"markdown link still works", "[a](b.md)", []string{"b.md"}},
		{"both styles", "[a](b.md) and [[c]]", []string{"b.md", "c.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMarkdownLinks(tt.content)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDiscoverBrainAbsent(t *testing.T) {
	dir := t.TempDir()
	if got := discoverBrain(dir); got != nil {
		t.Errorf("project without brain/ should return nil, got %d files", len(got))
	}
	if got := discoverBrain(""); got != nil {
		t.Errorf("empty project name should return nil, got %d files", len(got))
	}
}

func TestDiscoverBrainCategories(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("brain/wiki/lyra-foundation.md", "# Lyra\nlinks to [[vex-character-framework]]\n")
	write("brain/raw/index.md", "# Raw\n")
	write("brain/raw/sessions/2026-05-10-a-decision.md", "# Decision\n")
	write("brain/raw/corrections/2026-08-18-a-mistake.md", "# Mistake\n")
	// machine state must NOT be surfaced
	write("brain/_brain/brain_state.json", "{}")
	write("brain/_brain/GRAPH_REPORT.md", "# Graph\n")

	got := discoverBrain(dir)
	byCat := map[string]int{}
	for _, f := range got {
		byCat[f.Category]++
	}
	if byCat["brain-wiki"] != 1 {
		t.Errorf("brain-wiki: got %d, want 1", byCat["brain-wiki"])
	}
	if byCat["brain-raw"] != 2 { // raw/index.md + raw/sessions/*
		t.Errorf("brain-raw: got %d, want 2", byCat["brain-raw"])
	}
	if byCat["brain-corrections"] != 1 {
		t.Errorf("brain-corrections: got %d, want 1", byCat["brain-corrections"])
	}
	for _, f := range got {
		if filepath.Base(filepath.Dir(f.Path)) == "_brain" {
			t.Errorf("_brain machine state must be excluded, but got %s", f.Path)
		}
	}
}
