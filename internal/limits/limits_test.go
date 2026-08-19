package limits

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These mirror nirvana-claude-watch's own tests case for case. The daemon is
// being retired in favour of this package, so its behaviour has to survive the
// move intact.

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func snap(kind, scope string, percent int64, resetsAt string) Snapshot {
	return Snapshot{Kind: kind, ScopeModel: scope, Percent: percent, ResetsAt: ts(resetsAt)}
}

func bands() []int { return []int{80, 95} }

func TestFirstPollIsSilent(t *testing.T) {
	st := NewState()
	ev := Diff(st, []Snapshot{snap("session", "", 50, "2026-07-12T02:00:00Z")},
		ts("2026-07-11T20:00:00Z"), bands())
	if len(ev) != 0 {
		t.Fatalf("first poll must say nothing, got %+v", ev)
	}
	if len(st.Limits) != 1 {
		t.Fatal("first poll must still record the baseline")
	}
}

func TestRolloverPastTheResetTimeIsAReset(t *testing.T) {
	st := NewState()
	st.Limits = []Snapshot{snap("weekly_all", "", 90, "2026-07-12T18:00:00Z")}
	ev := Diff(st, []Snapshot{snap("weekly_all", "", 0, "2026-07-19T18:00:00Z")},
		ts("2026-07-12T18:05:00Z"), bands())
	if len(ev) != 1 || ev[0].Kind != KindReset || ev[0].Limit != "weekly_all" {
		t.Fatalf("want one reset event, got %+v", ev)
	}
}

func TestRolloverAlreadyAnnouncedByTheTimerIsSuppressed(t *testing.T) {
	st := NewState()
	st.Limits = []Snapshot{snap("weekly_all", "", 90, "2026-07-12T18:00:00Z")}
	st.FiredResets["weekly_all"] = ts("2026-07-12T18:00:00Z")
	ev := Diff(st, []Snapshot{snap("weekly_all", "", 0, "2026-07-19T18:00:00Z")},
		ts("2026-07-12T18:05:00Z"), bands())
	if len(ev) != 0 {
		t.Fatalf("the timer already said it; poll must stay quiet, got %+v", ev)
	}
}

func TestAFutureShiftIsAScheduleChange(t *testing.T) {
	st := NewState()
	st.Limits = []Snapshot{snap("weekly_all", "", 10, "2026-07-19T18:00:00Z")}
	ev := Diff(st, []Snapshot{snap("weekly_all", "", 10, "2026-07-15T18:00:00Z")},
		ts("2026-07-12T19:00:00Z"), bands())
	if len(ev) != 1 || ev[0].Kind != KindScheduleChanged {
		t.Fatalf("want a schedule change, got %+v", ev)
	}
	if !ev[0].Old.Equal(ts("2026-07-19T18:00:00Z")) || !ev[0].New.Equal(ts("2026-07-15T18:00:00Z")) {
		t.Fatalf("old/new not carried: %+v", ev[0])
	}
}

func TestSubMinuteJitterIsIgnored(t *testing.T) {
	st := NewState()
	st.Limits = []Snapshot{snap("session", "", 10, "2026-07-12T02:00:00Z")}
	ev := Diff(st, []Snapshot{snap("session", "", 12, "2026-07-12T02:00:30Z")},
		ts("2026-07-11T23:00:00Z"), bands())
	if len(ev) != 0 {
		t.Fatalf("30s of server jitter is not a reschedule, got %+v", ev)
	}
}

func TestLimitsAppearAndVanish(t *testing.T) {
	st := NewState()
	st.Limits = []Snapshot{snap("weekly_all", "", 0, "2026-07-19T18:00:00Z")}
	ev := Diff(st, []Snapshot{
		snap("weekly_all", "", 0, "2026-07-19T18:00:00Z"),
		snap("weekly_scoped", "Fable", 0, "2026-07-19T18:00:00Z"),
	}, ts("2026-07-13T00:00:00Z"), bands())
	if len(ev) != 1 || ev[0].Kind != KindAppeared || ev[0].Limit != "weekly_scoped:Fable" {
		t.Fatalf("want the scoped limit to appear, got %+v", ev)
	}

	ev = Diff(st, []Snapshot{snap("weekly_all", "", 0, "2026-07-19T18:00:00Z")},
		ts("2026-07-14T00:00:00Z"), bands())
	if len(ev) != 1 || ev[0].Kind != KindVanished || ev[0].Limit != "weekly_scoped:Fable" {
		t.Fatalf("want the scoped limit to vanish, got %+v", ev)
	}
}

func TestThresholdCrossingIsEdgeTriggered(t *testing.T) {
	st := NewState()
	st.Limits = []Snapshot{snap("session", "", 70, "2026-07-12T02:00:00Z")}

	ev := Diff(st, []Snapshot{snap("session", "", 85, "2026-07-12T02:00:00Z")},
		ts("2026-07-11T22:00:00Z"), bands())
	if len(ev) != 1 || ev[0].Kind != KindApproaching || ev[0].Threshold != 80 || ev[0].Percent != 85 {
		t.Fatalf("want one crossing of 80, got %+v", ev)
	}

	// Still above 80: silent.
	ev = Diff(st, []Snapshot{snap("session", "", 88, "2026-07-12T02:00:00Z")},
		ts("2026-07-11T22:20:00Z"), bands())
	if len(ev) != 0 {
		t.Fatalf("staying in the same band must not re-fire, got %+v", ev)
	}

	// Clears 95: one more.
	ev = Diff(st, []Snapshot{snap("session", "", 97, "2026-07-12T02:00:00Z")},
		ts("2026-07-11T22:40:00Z"), bands())
	if len(ev) != 1 || ev[0].Threshold != 95 || ev[0].Percent != 97 {
		t.Fatalf("want a crossing of 95, got %+v", ev)
	}
}

func TestJumpingPastSeveralBandsFiresOnceAtTheHighest(t *testing.T) {
	st := NewState()
	st.Limits = []Snapshot{snap("weekly_all", "", 40, "2026-07-19T18:00:00Z")}
	ev := Diff(st, []Snapshot{snap("weekly_all", "", 99, "2026-07-19T18:00:00Z")},
		ts("2026-07-13T00:00:00Z"), bands())
	if len(ev) != 1 || ev[0].Threshold != 95 {
		t.Fatalf("one event at the highest band, got %+v", ev)
	}
}

func TestRolloverClearsThresholdMemory(t *testing.T) {
	st := NewState()
	st.Limits = []Snapshot{snap("session", "", 97, "2026-07-12T02:00:00Z")}
	st.NotifiedThresholds["session"] = 95
	Diff(st, []Snapshot{snap("session", "", 1, "2026-07-12T07:00:00Z")},
		ts("2026-07-12T02:05:00Z"), bands())
	if _, ok := st.NotifiedThresholds["session"]; ok {
		t.Fatal("a new window starts with a clean threshold slate")
	}
}

func TestDedupeMapsDoNotGrowForever(t *testing.T) {
	st := NewState()
	st.FiredResets["gone"] = ts("2026-07-01T00:00:00Z")
	st.NotifiedThresholds["gone"] = 80
	st.Limits = []Snapshot{snap("session", "", 10, "2026-07-12T02:00:00Z")}
	Diff(st, []Snapshot{snap("session", "", 10, "2026-07-12T02:00:00Z")},
		ts("2026-07-11T22:00:00Z"), bands())
	if _, ok := st.FiredResets["gone"]; ok {
		t.Fatal("dedupe entries for limits that no longer exist must be dropped")
	}
	if _, ok := st.NotifiedThresholds["gone"]; ok {
		t.Fatal("threshold memory for a vanished limit must be dropped")
	}
}

func TestScopedLimitsHaveDistinctIdentities(t *testing.T) {
	if snap("weekly_scoped", "Fable", 0, "2026-07-19T18:00:00Z").ID() != "weekly_scoped:Fable" {
		t.Fatal("scoped id must include the model")
	}
	if snap("weekly_all", "", 0, "2026-07-19T18:00:00Z").ID() != "weekly_all" {
		t.Fatal("unscoped id is just the kind")
	}
}

func TestParseSkipsUnusableEntriesWithoutLosingTheRest(t *testing.T) {
	// A scoped limit with a null resets_at is something the API really emits;
	// it must not blind the whole poll.
	raw := json.RawMessage(`{"limits":[
	  {"kind":"session","percent":38,"resets_at":"2026-08-19T04:19:59Z"},
	  {"kind":"weekly_scoped","percent":41,"resets_at":null,"scope":{"model":{"display_name":"Fable"}}},
	  {"kind":"weekly_all","percent":44,"resets_at":"2026-08-23T17:59:59Z"},
	  {"garbage":true}
	]}`)
	got := Parse(raw)
	if len(got) != 2 {
		t.Fatalf("want the two usable limits, got %+v", got)
	}
	if got[0].ID() != "session" || got[1].ID() != "weekly_all" {
		t.Fatalf("unexpected ids: %+v", got)
	}
}

func TestParseReadsTheScopeModel(t *testing.T) {
	raw := json.RawMessage(`{"limits":[
	  {"kind":"weekly_scoped","percent":41,"resets_at":"2026-08-23T17:59:59Z","scope":{"model":{"display_name":"Fable"}}}
	]}`)
	got := Parse(raw)
	if len(got) != 1 || got[0].ID() != "weekly_scoped:Fable" {
		t.Fatalf("scope not read: %+v", got)
	}
}

func TestParseOfGarbageIsEmptyNotAPanic(t *testing.T) {
	if got := Parse(json.RawMessage(`not json`)); got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}

func TestNextResetPicksTheSoonestFutureWindow(t *testing.T) {
	now := ts("2026-07-12T00:00:00Z")
	snaps := []Snapshot{
		snap("weekly_all", "", 0, "2026-07-19T18:00:00Z"),
		snap("session", "", 0, "2026-07-12T02:00:00Z"),
		snap("stale", "", 0, "2026-07-11T00:00:00Z"), // already past
	}
	id, at, ok := NextReset(snaps, now)
	if !ok || id != "session" || !at.Equal(ts("2026-07-12T02:00:00Z")) {
		t.Fatalf("got %q %v %v", id, at, ok)
	}
}

func TestNextResetWithNothingAheadReportsNone(t *testing.T) {
	_, _, ok := NextReset([]Snapshot{snap("session", "", 0, "2026-07-11T00:00:00Z")},
		ts("2026-07-12T00:00:00Z"))
	if ok {
		t.Fatal("a window already past is not the next reset")
	}
}

func TestMarkFiredIsIdempotentPerWindow(t *testing.T) {
	st := NewState()
	at := ts("2026-07-12T02:00:00Z")
	if !st.MarkFired("session", at) {
		t.Fatal("first announcement should be allowed")
	}
	if st.MarkFired("session", at) {
		t.Fatal("the same window must not be announced twice")
	}
	if !st.MarkFired("session", ts("2026-07-12T07:00:00Z")) {
		t.Fatal("the next window is a new announcement")
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := NewState()
	st.Limits = []Snapshot{snap("session", "", 42, "2026-07-12T02:00:00Z")}
	st.FiredResets["session"] = ts("2026-07-11T21:00:00Z")
	st.NotifiedThresholds["session"] = 80
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}

	back := LoadState(path)
	if len(back.Limits) != 1 || back.Limits[0].Percent != 42 {
		t.Fatalf("limits not restored: %+v", back.Limits)
	}
	if back.NotifiedThresholds["session"] != 80 {
		t.Fatal("threshold memory not restored")
	}
	// The point of persisting: the poll after a restart is a comparison, not
	// a silent first run.
	ev := Diff(back, []Snapshot{snap("session", "", 0, "2026-07-12T07:00:00Z")},
		ts("2026-07-12T02:05:00Z"), bands())
	if len(ev) != 1 || ev[0].Kind != KindReset {
		t.Fatalf("a reset that happened across a restart must still be reported, got %+v", ev)
	}
}

func TestCorruptStateFileDegradesToEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{ truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := LoadState(path)
	if len(st.Limits) != 0 || st.FiredResets == nil {
		t.Fatalf("corrupt state must load as usable-empty: %+v", st)
	}
}

func TestRenderedMessagesReadCorrectly(t *testing.T) {
	cases := []struct {
		ev          Event
		wantTitle   string
		wantUrgency string
	}{
		{Event{Kind: KindReset, Limit: "session"}, "Claude session limit reset", "normal"},
		{Event{Kind: KindReset, Limit: "weekly_all"}, "Claude weekly limit reset", "normal"},
		{Event{Kind: KindScheduleChanged, Limit: "weekly_all",
			Old: ts("2026-07-19T18:00:00Z"), New: ts("2026-07-15T18:00:00Z")},
			"Claude weekly limit schedule changed", "critical"},
		{Event{Kind: KindVanished, Limit: "weekly_scoped:Fable"},
			"Claude limit removed: Fable", "critical"},
		{Event{Kind: KindApproaching, Limit: "session", Threshold: 95, Percent: 97},
			"Claude session usage past 95%", "critical"},
		{Event{Kind: KindApproaching, Limit: "session", Threshold: 80, Percent: 85},
			"Claude session usage past 80%", "normal"},
	}
	for _, c := range cases {
		got := Render(c.ev)
		if got.Title != c.wantTitle {
			t.Errorf("title = %q, want %q", got.Title, c.wantTitle)
		}
		if got.Urgency != c.wantUrgency {
			t.Errorf("%s urgency = %q, want %q", c.wantTitle, got.Urgency, c.wantUrgency)
		}
	}
}

func TestScheduleChangeBodyNamesBothTimes(t *testing.T) {
	m := Render(Event{Kind: KindScheduleChanged, Limit: "weekly_all",
		Old: ts("2026-07-19T18:00:00Z"), New: ts("2026-07-15T18:00:00Z")})
	for _, want := range []string{"2026-07-19 18:00 UTC", "2026-07-15 18:00 UTC"} {
		if !contains(m.Body, want) {
			t.Fatalf("body %q missing %q", m.Body, want)
		}
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
