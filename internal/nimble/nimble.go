// Package nimble reads NIMBLE's on-disk PRD state.
//
// NIMBLE orchestrates multi-task PRD execution and keeps its whole view of a
// run in files under a repository's .nimble/prds/{NNN_slug}/ directory:
// tasks.yaml (what the work is), execution_plan.yaml (which wave each task
// runs in) and status.json (what has happened so far). Its own
// /nimble:dashboard command re-derives its report from those three files on
// every invocation, so reading them directly is the whole integration — there
// is no live protocol to speak.
//
// # No current repository
//
// claumon watches sessions wherever they run, so there is no single "current
// repo" to look in. Discovery therefore starts from the project directories
// claumon already knows about — the CWD of each recent session — and looks for
// a .nimble/prds/ folder under each, grouping what it finds by project.
//
// # Absence is not failure
//
// A machine with no NIMBLE installed, a project with no .nimble/ directory and
// a PRD that has tasks.yaml but no status.json yet are all normal states.
// Every read here is allowed to fail: a missing or malformed file drops that
// file's contribution and nothing else, because a panel that 500s on one bad
// YAML file is worse than one that shows the other PRDs.
package nimble

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fabioconcina/claumon/internal/parser"
	"gopkg.in/yaml.v3"
)

// StaleAfter is how long a task may sit in a non-terminal state before it is
// reported as stale. NIMBLE itself defines no threshold; a day is picked
// because a wave that has not moved since yesterday is not still working, it
// is abandoned.
const StaleAfter = 24 * time.Hour

// Project is one project directory and the PRDs found under its .nimble/prds/.
type Project struct {
	// Path is the project directory, as seen in a session's CWD.
	Path string `json:"path"`
	PRDs []PRD  `json:"prds"`
}

// PRD is one .nimble/prds/{NNN_slug}/ directory, as far as it can be read.
type PRD struct {
	// Slug is NIMBLE's own name for the PRD, falling back to the directory
	// name when no file states one.
	Slug string `json:"prd_slug"`
	// Dir is the PRD directory's absolute path.
	Dir string `json:"dir"`
	// Status is status.json's top-level status (draft, ready, executing...),
	// or "unknown" when there is no status.json to read it from.
	Status        string `json:"status"`
	ExecutionMode string `json:"execution_mode,omitempty"`
	FeatureBranch string `json:"feature_branch,omitempty"`
	// TaskCount is the number of tasks the PRD defines.
	TaskCount int `json:"task_count"`
	// Waves is wave progress, ordered by wave number.
	Waves []Wave `json:"waves"`
	// Alerts are the task states that need someone to look.
	Alerts []Alert `json:"alerts"`
}

// Wave is one execution wave's progress.
type Wave struct {
	Number int `json:"number"`
	// Status is status.json's own word for the wave (pending, running,
	// complete, blocked), or "unknown" when it does not carry one. Unknown is
	// reported as unknown rather than assumed healthy.
	Status string `json:"status"`
	// Tasks is how many tasks the wave contains, Done how many of them
	// status.json reports as done.
	Tasks int `json:"tasks"`
	Done  int `json:"done"`
}

// Alert is a task state that needs attention: blocked, crashed, failed, or
// stalled with no progress for longer than StaleAfter.
type Alert struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title,omitempty"`
	// Kind is BLOCKED, CRASHED, FAILED or STALE.
	Kind string `json:"kind"`
	// Status is the task status the alert was derived from.
	Status string `json:"status"`
}

// alertStates are the task statuses that are an alert in themselves,
// independent of how long the task has been sitting in them.
var alertStates = map[string]bool{"blocked": true, "crashed": true, "failed": true}

// statusFile is status.json. Only the fields this panel shows are named; the
// rest of the schema is deliberately ignored, since it is NIMBLE's to change.
type statusFile struct {
	PRDSlug string `json:"prd_slug"`
	// Slug is the older spelling of prd_slug, still written by PRDs that never
	// got past the draft stage.
	Slug          string `json:"slug"`
	Status        string `json:"status"`
	ExecutionMode string `json:"execution_mode"`
	FeatureBranch string `json:"feature_branch"`
	Waves         map[string]struct {
		Status string `json:"status"`
	} `json:"waves"`
	Tasks map[string]statusTask `json:"tasks"`
}

type statusTask struct {
	Status string `json:"status"`
	Wave   int    `json:"wave"`
	// Timestamps are read as strings and parsed leniently: an unparseable one
	// means "no idea when this last moved", not a broken status.json.
	UpdatedAt string `json:"updated_at"`
	StartedAt string `json:"started_at"`
}

// tasksFile is tasks.yaml. Its descriptions are block scalars, which is why
// this is unmarshalled rather than scanned.
type tasksFile struct {
	Metadata struct {
		PRDSlug string `yaml:"prd_slug"`
	} `yaml:"metadata"`
	Tasks []struct {
		ID    string `yaml:"id"`
		Title string `yaml:"title"`
	} `yaml:"tasks"`
}

// planFile is execution_plan.yaml: wave number to the task ids in it.
type planFile struct {
	Waves map[int][]string `yaml:"waves"`
}

// Discover returns every project directory claumon has seen a session in that
// has at least one PRD under .nimble/prds/, each with its PRDs.
//
// limit bounds how many recent sessions are scanned for project directories;
// it defaults to 500, matching the fleet view. A machine that has never run
// Claude Code has no projects directory at all: that is an empty result, not
// an error, so a first-run install gets an empty panel rather than a 500.
func Discover(claudeDir string, limit int) ([]Project, error) {
	if limit <= 0 {
		limit = 500
	}
	summaries, err := parser.DiscoverRecentSessions(claudeDir, limit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Project{}, nil
		}
		return nil, err
	}

	// Hundreds of sessions share a handful of project directories; scan each
	// one once.
	seen := make(map[string]bool)
	var dirs []string
	for _, s := range summaries {
		if s == nil || s.CWD == "" || seen[s.CWD] {
			continue
		}
		seen[s.CWD] = true
		dirs = append(dirs, s.CWD)
	}
	sort.Strings(dirs)

	projects := []Project{}
	for _, dir := range dirs {
		prds := ScanProject(dir)
		if len(prds) == 0 {
			continue
		}
		projects = append(projects, Project{Path: dir, PRDs: prds})
	}
	return projects, nil
}

// ScanProject reads every PRD under one project directory's .nimble/prds/.
// A project with no .nimble/ directory, or one whose directory has since been
// deleted, yields an empty slice.
func ScanProject(dir string) []PRD {
	entries, err := filepath.Glob(filepath.Join(dir, ".nimble", "prds", "*"))
	if err != nil {
		return []PRD{}
	}
	prds := []PRD{}
	for _, entry := range entries {
		if info, err := os.Stat(entry); err != nil || !info.IsDir() {
			continue
		}
		prds = append(prds, readPRD(entry))
	}
	return prds
}

// readPRD assembles one PRD from whichever of its three state files can be
// read. Each is independently optional: a PRD still being planned has
// tasks.yaml but no status.json, and must still render.
func readPRD(dir string) PRD {
	var st statusFile
	var tasks tasksFile
	var plan planFile
	hasStatus := readFile(filepath.Join(dir, "status.json"), json.Unmarshal, &st)
	readFile(filepath.Join(dir, "tasks.yaml"), yaml.Unmarshal, &tasks)
	readFile(filepath.Join(dir, "execution_plan.yaml"), yaml.Unmarshal, &plan)

	prd := PRD{
		Slug:          filepath.Base(dir),
		Dir:           dir,
		Status:        "unknown",
		ExecutionMode: st.ExecutionMode,
		FeatureBranch: st.FeatureBranch,
		TaskCount:     len(tasks.Tasks),
	}
	for _, slug := range []string{tasks.Metadata.PRDSlug, st.Slug, st.PRDSlug} {
		if slug != "" {
			prd.Slug = slug
		}
	}
	if hasStatus && st.Status != "" {
		prd.Status = st.Status
	}
	if prd.TaskCount == 0 {
		prd.TaskCount = len(st.Tasks)
	}

	titles := make(map[string]string, len(tasks.Tasks))
	for _, t := range tasks.Tasks {
		titles[t.ID] = t.Title
	}
	prd.Waves = buildWaves(plan, st)
	prd.Alerts = buildAlerts(st, titles)
	return prd
}

// buildWaves merges the plan's wave membership with status.json's per-wave
// status. Either source may be missing: a PRD with no execution_plan.yaml
// still has waves if its task entries name one.
func buildWaves(plan planFile, st statusFile) []Wave {
	members := make(map[int][]string)
	placed := make(map[string]bool)
	for n, ids := range plan.Waves {
		members[n] = append(members[n], ids...)
		for _, id := range ids {
			placed[id] = true
		}
	}
	for id, t := range st.Tasks {
		if t.Wave <= 0 || placed[id] {
			continue
		}
		members[t.Wave] = append(members[t.Wave], id)
	}

	numbers := make([]int, 0, len(members))
	for n := range members {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)

	waves := []Wave{}
	for _, n := range numbers {
		w := Wave{Number: n, Status: "unknown", Tasks: len(members[n])}
		if s, ok := st.Waves[strconv.Itoa(n)]; ok && s.Status != "" {
			w.Status = s.Status
		}
		for _, id := range members[n] {
			if strings.EqualFold(st.Tasks[id].Status, "done") {
				w.Done++
			}
		}
		waves = append(waves, w)
	}
	return waves
}

// buildAlerts reports the task states that need someone to look: the ones that
// say so outright, plus any non-terminal task that has not moved in StaleAfter.
// A task whose status field is missing or empty is reported as unknown rather
// than passed over as healthy.
func buildAlerts(st statusFile, titles map[string]string) []Alert {
	ids := make([]string, 0, len(st.Tasks))
	for id := range st.Tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	alerts := []Alert{}
	for _, id := range ids {
		t := st.Tasks[id]
		status := strings.ToLower(strings.TrimSpace(t.Status))
		if status == "" {
			status = "unknown"
		}
		switch {
		case alertStates[status]:
			alerts = append(alerts, Alert{TaskID: id, Title: titles[id], Kind: strings.ToUpper(status), Status: status})
		case status != "done" && isStale(t):
			alerts = append(alerts, Alert{TaskID: id, Title: titles[id], Kind: "STALE", Status: status})
		}
	}
	return alerts
}

// isStale reports whether a task's last known movement is older than
// StaleAfter. A task with no usable timestamp is never stale: NIMBLE does not
// always write one, and inventing an age would turn every queued task into an
// alert.
func isStale(t statusTask) bool {
	for _, ts := range []string{t.UpdatedAt, t.StartedAt} {
		if ts == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}
		return time.Since(parsed) > StaleAfter
	}
	return false
}

// readFile unmarshals one state file, reporting whether it was usable. A
// missing file is silent — it is the normal state of a half-planned PRD — and
// a malformed one is logged and skipped, leaving the rest of the PRD readable.
func readFile(path string, unmarshal func([]byte, any) error, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[nimble] Failed to read %s: %v", path, err)
		}
		return false
	}
	if err := unmarshal(data, v); err != nil {
		log.Printf("[nimble] Failed to parse %s: %v", path, err)
		return false
	}
	return true
}
