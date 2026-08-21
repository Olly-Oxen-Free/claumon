package nimble

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanProjectEmpty(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{
			name:  "no .nimble directory",
			setup: func(t *testing.T, dir string) {},
		},
		{
			name: "empty .nimble/prds",
			setup: func(t *testing.T, dir string) {
				mkdir(t, filepath.Join(dir, ".nimble", "prds"))
			},
		},
		{
			name: "prds contains a file, not a PRD directory",
			setup: func(t *testing.T, dir string) {
				mkdir(t, filepath.Join(dir, ".nimble", "prds"))
				write(t, filepath.Join(dir, ".nimble", "prds", "README.md"), "not a prd")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			if prds := ScanProject(dir); len(prds) != 0 {
				t.Fatalf("want no PRDs, got %d", len(prds))
			}
		})
	}
}

func TestScanProjectMissingDirectory(t *testing.T) {
	// A project directory that has since been deleted is skipped, not an error.
	if prds := ScanProject(filepath.Join(t.TempDir(), "gone")); len(prds) != 0 {
		t.Fatalf("want no PRDs, got %d", len(prds))
	}
}

// TestScanProjectPopulated points at this repository's own PRD directory: a
// real, on-disk NIMBLE state with tasks.yaml and execution_plan.yaml but no
// populated task states, which is the "readable, no alerts" path.
func TestScanProjectPopulated(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, ".nimble", "prds")); err != nil {
		t.Skip("no .nimble/prds in this checkout")
	}
	prds := ScanProject(root)
	if len(prds) == 0 {
		t.Fatal("want at least one PRD from this repository's own .nimble/prds")
	}
	var found *PRD
	for i := range prds {
		if prds[i].Slug == "timeline-feature-merger" {
			found = &prds[i]
		}
	}
	if found == nil {
		t.Fatalf("want the timeline-feature-merger PRD, got %+v", prds)
	}
	if found.Status == "" {
		t.Error("want a status, got empty")
	}
	if found.TaskCount == 0 {
		t.Error("want tasks from tasks.yaml, got none")
	}
	if len(found.Waves) == 0 {
		t.Error("want waves from execution_plan.yaml, got none")
	}
	if len(found.Alerts) != 0 {
		t.Errorf("want no alerts for a PRD with no task states, got %+v", found.Alerts)
	}
}

func TestReadPRDAlertsAndWaves(t *testing.T) {
	dir := t.TempDir()
	prdDir := filepath.Join(dir, ".nimble", "prds", "001_example")
	mkdir(t, prdDir)
	write(t, filepath.Join(prdDir, "tasks.yaml"), `
metadata:
  prd_slug: example
tasks:
  - id: "1a"
    title: "First task"
    description: >
      A block scalar, which is why this is parsed as YAML
      rather than scanned line by line.
  - id: "1b"
    title: "Second task"
  - id: "2a"
    title: "Third task"
  - id: "2b"
    title: "Fourth task"
`)
	write(t, filepath.Join(prdDir, "execution_plan.yaml"), `
waves:
  1: ["1a", "1b"]
  2: ["2a", "2b"]
`)
	stale := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	write(t, filepath.Join(prdDir, "status.json"), `{
  "prd_slug": "example",
  "status": "executing",
  "execution_mode": "feature-branch",
  "feature_branch": "feature/example",
  "waves": { "1": { "status": "running" }, "2": { "status": "pending" } },
  "tasks": {
    "1a": { "status": "done", "wave": 1 },
    "1b": { "status": "blocked", "wave": 1 },
    "2a": { "status": "running", "wave": 2, "updated_at": "`+stale+`" },
    "2b": { "status": "crashed", "wave": 2 }
  }
}`)

	prds := ScanProject(dir)
	if len(prds) != 1 {
		t.Fatalf("want 1 PRD, got %d", len(prds))
	}
	p := prds[0]
	if p.Slug != "example" || p.Status != "executing" || p.FeatureBranch != "feature/example" {
		t.Errorf("unexpected PRD header: %+v", p)
	}
	if p.TaskCount != 4 {
		t.Errorf("want 4 tasks, got %d", p.TaskCount)
	}

	want := []Wave{
		{Number: 1, Status: "running", Tasks: 2, Done: 1},
		{Number: 2, Status: "pending", Tasks: 2, Done: 0},
	}
	if len(p.Waves) != len(want) {
		t.Fatalf("want %d waves, got %+v", len(want), p.Waves)
	}
	for i, w := range want {
		if p.Waves[i] != w {
			t.Errorf("wave %d: want %+v, got %+v", w.Number, w, p.Waves[i])
		}
	}

	wantAlerts := []Alert{
		{TaskID: "1b", Title: "Second task", Kind: "BLOCKED", Status: "blocked"},
		{TaskID: "2a", Title: "Third task", Kind: "STALE", Status: "running"},
		{TaskID: "2b", Title: "Fourth task", Kind: "CRASHED", Status: "crashed"},
	}
	if len(p.Alerts) != len(wantAlerts) {
		t.Fatalf("want %d alerts, got %+v", len(wantAlerts), p.Alerts)
	}
	for i, a := range wantAlerts {
		if p.Alerts[i] != a {
			t.Errorf("alert %d: want %+v, got %+v", i, a, p.Alerts[i])
		}
	}
}

// A PRD mid-planning has tasks.yaml and nothing else. It must still render,
// with an unknown status rather than an assumed healthy one.
func TestReadPRDStatusFileMissing(t *testing.T) {
	dir := t.TempDir()
	prdDir := filepath.Join(dir, ".nimble", "prds", "002_planning")
	mkdir(t, prdDir)
	write(t, filepath.Join(prdDir, "tasks.yaml"), "tasks:\n  - id: \"1a\"\n    title: \"Only task\"\n")

	prds := ScanProject(dir)
	if len(prds) != 1 {
		t.Fatalf("want 1 PRD, got %d", len(prds))
	}
	p := prds[0]
	if p.Slug != "002_planning" {
		t.Errorf("want the directory name as the slug, got %q", p.Slug)
	}
	if p.Status != "unknown" {
		t.Errorf("want status unknown, got %q", p.Status)
	}
	if p.TaskCount != 1 || len(p.Waves) != 0 || len(p.Alerts) != 0 {
		t.Errorf("unexpected PRD: %+v", p)
	}
}

// Malformed state drops that one file's contribution and keeps the rest.
func TestReadPRDMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	prdDir := filepath.Join(dir, ".nimble", "prds", "003_broken")
	mkdir(t, prdDir)
	write(t, filepath.Join(prdDir, "tasks.yaml"), "tasks: [ this is not: valid yaml")
	write(t, filepath.Join(prdDir, "status.json"), `{"prd_slug": "broken", "status": "ready", "tasks": {}}`)

	prds := ScanProject(dir)
	if len(prds) != 1 {
		t.Fatalf("want 1 PRD, got %d", len(prds))
	}
	if p := prds[0]; p.Slug != "broken" || p.Status != "ready" || p.TaskCount != 0 {
		t.Errorf("unexpected PRD: %+v", p)
	}
}

// A machine that has never run Claude Code has no projects directory: an empty
// panel, not an error.
func TestDiscoverNoProjectsDirectory(t *testing.T) {
	projects, err := Discover(filepath.Join(t.TempDir(), "no-claude-dir"), 0)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("want no projects, got %+v", projects)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
