package timeline

import (
	"bufio"
	"bytes"
	"os"
	"sync"
	"time"
)

// A session's bar is drawn from its first event to its last, which says a
// session existed across that stretch but not that anything was happening in
// it. Most long sessions are mostly gaps: a burst of work, an hour away, a
// burst on return. Drawn as one solid bar those read identically to an hour of
// continuous work.
//
// Activity splits a session's span by what the transcript actually shows:
//
//   - Active: events close enough together to be one working stretch.
//   - Idle: a lull long enough that the prompt cache has expired, but not long
//     enough to call the session put down. Drawn dashed — the session is open
//     and the thread is intact, but the next message pays full price again.
//   - A break: a lull long enough that the session was left. Nothing is drawn,
//     so the bar stops and starts again where work resumed.
//
// The distinction between the last two is a judgement about intent, and the
// thresholds below are where that judgement is made.
// A session goes cold when its prompt cache expires, and the TTL that governs
// that is not a constant — it is a property of the session, recorded in the
// transcript. Every cached turn reports which bucket it wrote to:
//
//	"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":1385}
//
// so the threshold is read from the session rather than assumed for it. A
// session that never cached, or whose transcript predates these fields, falls
// back to the 5-minute default.
const (
	cold5m = 5 * time.Minute
	cold1h = time.Hour

	// breakAfter is how long a lull must run before the bar breaks outright.
	// Long enough to be past any pause inside a working session — a meal, a
	// meeting, the end of a day — so a bar that resumes after one is showing a
	// genuine return rather than a long think.
	//
	// This sits above the longest cache TTL by design: a break is a stronger
	// claim than a cold cache, so it must not be reachable before the line has
	// had the chance to go dashed first.
	breakAfter = 90 * time.Minute
)

// SpanKind distinguishes a drawn stretch of a session's bar.
type SpanKind string

const (
	// SpanActive is work: events close enough together to read as continuous.
	SpanActive SpanKind = "active"
	// SpanIdle is an open session sitting untouched past the cache TTL.
	SpanIdle SpanKind = "idle"
)

// Span is one drawn stretch of a session's life.
//
// Breaks are not spans: they are the absence of one between two spans, which
// is exactly how they should draw.
type Span struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Kind SpanKind  `json:"kind"`
}

// activityCache memoises a session's spans against the file they came from,
// on the same terms as burnCache: keyed by size so a growing live transcript
// is recomputed and a finished one is read once.
var activityCache sync.Map // path -> activityEntry

type activityEntry struct {
	size  int64
	spans []Span
}

// Activity reads a transcript's event times and reduces them to drawn spans.
//
// Only the timestamps are needed, so lines are located by a byte scan and the
// stamp is sliced out rather than decoded — these files reach 40MB and JSON
// decoding every line to read one field costs an order of magnitude more than
// the answer is worth.
//
// `end` bounds the last span for a session that is still running: its work
// stops at its last event, but the caller knows whether the session is still
// open and how far the window runs.
func Activity(path string, end time.Time, running bool) []Span {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if v, ok := activityCache.Load(path); ok {
		if e := v.(activityEntry); e.size == info.Size() {
			return e.spans
		}
	}

	stamps, cold := readStamps(path)
	spans := spansFrom(stamps, end, running, cold)
	activityCache.Store(path, activityEntry{size: info.Size(), spans: spans})
	return spans
}

// readStamps pulls every event time out of a transcript, in file order, and
// the cache TTL the session was actually using.
//
// Both come from the same pass: the file is large and reading it twice to
// learn two things about it would double the cost of the cheapest part of the
// fleet build.
func readStamps(path string) ([]time.Time, time.Duration) {
	f, err := os.Open(path)
	if err != nil {
		return nil, cold5m
	}
	defer f.Close()

	key := []byte(`"timestamp":"`)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	// Which TTL bucket this session writes to. Counted rather than taken from
	// the first hit: a session can carry a few turns of the other kind, and
	// the one it mostly uses is the one that governs how it goes cold.
	key1h := []byte(`"ephemeral_1h_input_tokens":`)
	key5m := []byte(`"ephemeral_5m_input_tokens":`)
	var n1h, n5m int

	var out []time.Time
	for sc.Scan() {
		b := sc.Bytes()
		if j := bytes.Index(b, key1h); j >= 0 && nonZeroAfter(b[j+len(key1h):]) {
			n1h++
		}
		if j := bytes.Index(b, key5m); j >= 0 && nonZeroAfter(b[j+len(key5m):]) {
			n5m++
		}
		i := bytes.Index(b, key)
		if i < 0 {
			continue
		}
		rest := b[i+len(key):]
		j := bytes.IndexByte(rest, '"')
		if j < 0 {
			continue
		}
		t, err := time.Parse(time.RFC3339, string(rest[:j]))
		if err != nil {
			continue
		}
		out = append(out, t.UTC())
	}
	if n1h > n5m {
		return out, cold1h
	}
	return out, cold5m
}

// nonZeroAfter reports whether the number starting at b is anything but zero.
// A turn that wrote no tokens to a bucket says nothing about which TTL the
// session uses, and every cached turn reports both buckets.
func nonZeroAfter(b []byte) bool {
	for _, c := range b {
		switch {
		case c == '0':
			continue
		case c >= '1' && c <= '9':
			return true
		default:
			return false
		}
	}
	return false
}

// spansFrom reduces ordered event times to drawn spans.
//
// A split session replays its parent's history with the original timestamps,
// so stamps are not guaranteed to be sorted; they are walked in order and any
// stamp that moves backwards is skipped rather than opening a span that ends
// before it starts.
func spansFrom(stamps []time.Time, end time.Time, running bool, coldAfter time.Duration) []Span {
	if len(stamps) == 0 {
		return nil
	}

	var spans []Span
	start := stamps[0]
	prev := stamps[0]

	flush := func(to time.Time) {
		if to.After(start) {
			spans = append(spans, Span{From: start, To: to, Kind: SpanActive})
		} else {
			// A stretch with one event in it has no width. It is still where
			// work happened, so it is kept as a zero-length span and the
			// renderer gives it its minimum width.
			spans = append(spans, Span{From: start, To: start, Kind: SpanActive})
		}
	}

	for _, t := range stamps[1:] {
		if t.Before(prev) {
			continue
		}
		gap := t.Sub(prev)
		switch {
		case gap >= breakAfter:
			// Long enough that the session was put down: close the stretch and
			// draw nothing until it resumes.
			flush(prev)
			start = t
		case gap >= coldAfter:
			// Long enough for the cache to expire but not to call it left:
			// close the stretch, then bridge the lull dashed so the line stays
			// continuous while showing it went cold.
			flush(prev)
			spans = append(spans, Span{From: prev, To: t, Kind: SpanIdle})
			start = t
		}
		prev = t
	}
	flush(prev)

	// A session still open with nothing happening in it: the tail from its last
	// event to now is the part a reader most wants marked, because it is the
	// one they can still act on.
	if running && end.After(prev) && end.Sub(prev) >= coldAfter {
		spans = append(spans, Span{From: prev, To: end, Kind: SpanIdle})
	}
	return spans
}
