package parser

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// SearchHit is one matching message inside a session transcript.
type SearchHit struct {
	SessionID string `json:"session_id"`
	Project   string `json:"project"`
	Path      string `json:"path"`
	Role      string `json:"role"`
	Timestamp string `json:"timestamp"`
	Snippet   string `json:"snippet"`
	Field     string `json:"field"` // "text" or "thinking"
}

// SearchResult is the response for one query.
type SearchResult struct {
	Query        string      `json:"query"`
	Hits         []SearchHit `json:"hits"`
	FilesScanned int         `json:"files_scanned"`
	FilesMatched int         `json:"files_matched"`
	Truncated    bool        `json:"truncated"`
}

const (
	defaultSearchLimit = 100
	snippetContext     = 90
)

// SearchSessions finds messages matching query across every session transcript.
//
// Transcripts are large (well over a gigabyte in normal use) and JSON parsing all of
// them per query would be far too slow. Instead this does a raw case-insensitive byte
// scan first and only fully parses the files that actually contain the term — the scan
// costs tens of milliseconds across the whole corpus, and typically only a handful of
// files survive it.
//
// limit caps returned hits; pass 0 for the default.
func SearchSessions(claudeDir, query string, limit int) (*SearchResult, error) {
	res := &SearchResult{Query: query}
	needle := strings.TrimSpace(query)
	if needle == "" {
		return res, nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	lowerNeedle := []byte(strings.ToLower(needle))

	projectsDir := filepath.Join(claudeDir, "projects")

	// Collect paths first, then prefilter them in parallel. The scan is I/O bound over
	// a corpus that runs to gigabytes; doing it serially cost ~1.6s where the parallel
	// version is a few hundred milliseconds on the same data.
	var paths []string
	err := filepath.WalkDir(projectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil //nolint:nilerr // unreadable entries are skipped, not fatal
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return res, err
	}
	res.FilesScanned = len(paths)

	workers := runtime.NumCPU()
	if workers > len(paths) {
		workers = len(paths)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan string)
	hits := make(chan string, len(paths))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				data, rerr := os.ReadFile(path)
				if rerr != nil {
					continue
				}
				if containsFold(data, lowerNeedle) {
					hits <- path
				}
			}
		}()
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	close(hits)

	var candidates []string
	for p := range hits {
		candidates = append(candidates, p)
	}
	res.FilesMatched = len(candidates)
	sort.Strings(candidates)

	for _, path := range candidates {
		msgs, perr := ParseSessionDetail(path)
		if perr != nil {
			continue
		}
		sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		project := DecodeProjectDir(filepath.Base(filepath.Dir(path)))
		for _, m := range msgs {
			for _, f := range []struct{ name, body string }{{"text", m.Text}, {"thinking", m.Thinking}} {
				idx := indexFold(f.body, needle)
				if idx < 0 {
					continue
				}
				if len(res.Hits) >= limit {
					res.Truncated = true
					return res, nil
				}
				res.Hits = append(res.Hits, SearchHit{
					SessionID: sessionID,
					Project:   project,
					Path:      path,
					Role:      m.Role,
					Timestamp: m.Timestamp.Format("2006-01-02 15:04"),
					Snippet:   snippet(f.body, idx, len(needle)),
					Field:     f.name,
				})
			}
		}
	}
	return res, nil
}

// containsFold reports whether data contains needle, case-insensitively.
//
// needle must already be lowercase. This deliberately avoids bytes.ToLower on the
// haystack: the transcript corpus runs to gigabytes, and lowercasing every file
// allocated a full copy of each one, which dominated query time (3.8s vs 40ms for the
// same scan without the copy). Instead it seeks candidate start positions on the first
// byte in either case and compares in place.
func containsFold(data, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	first := needle[0]
	upper := first
	if first >= 'a' && first <= 'z' {
		upper = first - 32
	}
	for i := 0; i+len(needle) <= len(data); i++ {
		c := data[i]
		if c != first && c != upper {
			continue
		}
		if equalFoldASCII(data[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

// equalFoldASCII compares a haystack window against an already-lowercased needle.
func equalFoldASCII(a, lowerB []byte) bool {
	for i := range lowerB {
		c := a[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		if c != lowerB[i] {
			return false
		}
	}
	return true
}

// indexFold is a case-insensitive strings.Index.
func indexFold(haystack, needle string) int {
	return strings.Index(strings.ToLower(haystack), strings.ToLower(needle))
}

// snippet returns the match plus surrounding context, collapsed to one line.
func snippet(body string, idx, matchLen int) string {
	start := idx - snippetContext
	if start < 0 {
		start = 0
	}
	end := idx + matchLen + snippetContext
	if end > len(body) {
		end = len(body)
	}
	out := strings.Join(strings.Fields(body[start:end]), " ")
	if start > 0 {
		out = "..." + out
	}
	if end < len(body) {
		out += "..."
	}
	return out
}

// DecodeProjectDir turns an encoded ~/.claude/projects directory name into something
// readable for display. Kept deliberately simple: this is a label, not a path that
// gets opened, so the lossy "-" to "/" mapping is acceptable here.
func DecodeProjectDir(encoded string) string {
	return "/" + strings.ReplaceAll(strings.TrimPrefix(encoded, "-"), "-", "/")
}
