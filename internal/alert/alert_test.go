package alert

import (
	"testing"
	"time"
)

func enabled() Config {
	c := DefaultConfig()
	c.Enabled = true
	c.Desktop = false
	return c
}

// paced returns a config with the rate limit off, for the tests that are about
// dedupe rather than pacing.
func paced() Config {
	c := enabled()
	c.MinIntervalMins = -1
	return c
}

func TestProjectionBelowCapDoesNotAlert(t *testing.T) {
	f := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 82, Lower80Pct: 60, CurrentPct: 44}
	if _, ok := Evaluate(enabled(), f, time.Now()); ok {
		t.Fatal("projection under the cap must not alert")
	}
}

func TestCentralProjectionOverCapIsAtRisk(t *testing.T) {
	f := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 104, Lower80Pct: 88, CurrentPct: 74}
	a, ok := Evaluate(enabled(), f, time.Now())
	if !ok {
		t.Fatal("expected an alert")
	}
	if a.Level != LevelAtRisk {
		t.Fatalf("level = %q, want at_risk (lower bound is still under the cap)", a.Level)
	}
}

func TestLowerBoundOverCapIsLikely(t *testing.T) {
	f := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 130, Lower80Pct: 105, CurrentPct: 70}
	a, ok := Evaluate(enabled(), f, time.Now())
	if !ok {
		t.Fatal("expected an alert")
	}
	if a.Level != LevelLikely {
		t.Fatalf("level = %q, want likely", a.Level)
	}
	if a.Title != "Session limit will be reached" {
		t.Fatalf("title = %q", a.Title)
	}
}

func TestDisabledConfigNeverAlerts(t *testing.T) {
	f := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 200, Lower80Pct: 190, CurrentPct: 74}
	if _, ok := Evaluate(DefaultConfig(), f, time.Now()); ok {
		t.Fatal("default config is off and must stay silent")
	}
}

func TestGaugeFilterExcludesOtherGauges(t *testing.T) {
	c := enabled()
	c.Gauges = []string{"weekly"}
	f := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 200, Lower80Pct: 190, CurrentPct: 74}
	if _, ok := Evaluate(c, f, time.Now()); ok {
		t.Fatal("session alerts must be filtered out when only weekly is listed")
	}
}

func TestBodyCarriesTheNumbersAndETA(t *testing.T) {
	now := time.Date(2026, 8, 18, 20, 0, 0, 0, time.Local)
	f := Forecast{
		Gauge: "weekly", ResetAt: "r1",
		ProjectedPct: 112.4, Lower80Pct: 101, CurrentPct: 74.6,
		ETA: now.Add(3 * time.Hour),
	}
	a, ok := Evaluate(enabled(), f, now)
	if !ok {
		t.Fatal("expected an alert")
	}
	for _, want := range []string{"75%", "112%", "100%", "23:00"} {
		if !contains(a.Body, want) {
			t.Fatalf("body %q missing %q", a.Body, want)
		}
	}
}

func TestNotifierSendsOncePerWindow(t *testing.T) {
	n := NewNotifier(paced())
	f := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 104, Lower80Pct: 90, CurrentPct: 74}
	if _, ok := n.Consider(f, time.Now()); !ok {
		t.Fatal("first alert should be delivered")
	}
	if _, ok := n.Consider(f, time.Now()); ok {
		t.Fatal("same window must not alert twice")
	}
}

func TestNotifierReAlertsWhenRiskHardens(t *testing.T) {
	n := NewNotifier(paced())
	atRisk := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 104, Lower80Pct: 90, CurrentPct: 74}
	likely := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 130, Lower80Pct: 108, CurrentPct: 74}
	if _, ok := n.Consider(atRisk, time.Now()); !ok {
		t.Fatal("first alert should be delivered")
	}
	a, ok := n.Consider(likely, time.Now())
	if !ok {
		t.Fatal("escalation to likely should alert again")
	}
	if a.Level != LevelLikely {
		t.Fatalf("level = %q", a.Level)
	}
	// ...but not a third time, and not on the way back down.
	if _, ok := n.Consider(likely, time.Now()); ok {
		t.Fatal("repeat at the same level must be suppressed")
	}
	if _, ok := n.Consider(atRisk, time.Now()); ok {
		t.Fatal("easing back to at-risk must not re-alert")
	}
}

func TestNewWindowAlertsAgain(t *testing.T) {
	n := NewNotifier(paced())
	first := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 104, Lower80Pct: 90, CurrentPct: 74}
	second := Forecast{Gauge: "session", ResetAt: "r2", ProjectedPct: 104, Lower80Pct: 90, CurrentPct: 74}
	n.Consider(first, time.Now())
	if _, ok := n.Consider(second, time.Now()); !ok {
		t.Fatal("a new reset window is a new alert")
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

// The bug this pins: the API returns resets_at with sub-second jitter that
// differs on every poll, so the dedupe key was unique every time and the
// alert fired on every poll for hours.
func TestJitteringResetAtIsOneWindow(t *testing.T) {
	n := NewNotifier(paced())
	polls := []string{
		"2026-08-19T19:20:00.310465+00:00",
		"2026-08-19T19:19:59.866966+00:00",
		"2026-08-19T19:20:00.215847+00:00",
		"2026-08-19T19:19:59.510832+00:00",
	}
	sent := 0
	for _, r := range polls {
		f := Forecast{Gauge: "session", ResetAt: r, ProjectedPct: 150, Lower80Pct: 60, CurrentPct: 74}
		if _, ok := n.Consider(f, time.Now()); ok {
			sent++
		}
	}
	if sent != 1 {
		t.Fatalf("sent %d alerts for one window, want 1", sent)
	}
}

func TestNormalizeWindowRoundsToMinute(t *testing.T) {
	cases := map[string]string{
		"2026-08-19T19:20:00.310465+00:00": "2026-08-19T19:20:00Z",
		"2026-08-19T19:19:59.866966+00:00": "2026-08-19T19:20:00Z",
		"":                                 "",
		"not-a-time":                       "not-a-time",
	}
	for in, want := range cases {
		if got := NormalizeWindow(in); got != want {
			t.Errorf("NormalizeWindow(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEarlyWindowProjectionStaysQuiet(t *testing.T) {
	// 7% used projecting to 300% is the model extrapolating from a burst, not
	// news: the window has hours of slack and the estimate has not settled.
	f := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 300, Lower80Pct: 200, CurrentPct: 7}
	if _, ok := Evaluate(enabled(), f, time.Now()); ok {
		t.Fatal("a projection this early in the window must stay quiet")
	}
	f.CurrentPct = 74
	if _, ok := Evaluate(enabled(), f, time.Now()); !ok {
		t.Fatal("the same projection must alert once usage is materially along")
	}
}

func TestMinIntervalHoldsAcrossEscalation(t *testing.T) {
	n := NewNotifier(enabled()) // default 60-minute gap
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	atRisk := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 110, Lower80Pct: 90, CurrentPct: 74}
	likely := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 180, Lower80Pct: 140, CurrentPct: 80}

	if _, ok := n.Consider(atRisk, start); !ok {
		t.Fatal("first alert should be delivered")
	}
	// An escalation two minutes later is real, but it cannot change what the
	// reader does, so it waits.
	if _, ok := n.Consider(likely, start.Add(2*time.Minute)); ok {
		t.Fatal("escalation inside the interval must be held")
	}
	if _, ok := n.Consider(likely, start.Add(61*time.Minute)); !ok {
		t.Fatal("escalation should be delivered once the interval has passed")
	}
}

func TestMinIntervalIsPerGauge(t *testing.T) {
	n := NewNotifier(enabled())
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sess := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 150, Lower80Pct: 90, CurrentPct: 74}
	week := Forecast{Gauge: "weekly", ResetAt: "w1", ProjectedPct: 150, Lower80Pct: 90, CurrentPct: 74}
	if _, ok := n.Consider(sess, now); !ok {
		t.Fatal("session alert should be delivered")
	}
	// Two different limits are two different facts; one must not gag the other.
	if _, ok := n.Consider(week, now); !ok {
		t.Fatal("a weekly alert must not be blocked by a session alert")
	}
}

func TestZeroConfigGetsPacedDefaults(t *testing.T) {
	// A config file written before these fields existed must get the paced
	// behaviour, not the old every-poll one.
	c := Config{Enabled: true, CapPct: 100, Desktop: false}
	f := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 300, Lower80Pct: 200, CurrentPct: 7}
	if _, ok := Evaluate(c, f, time.Now()); ok {
		t.Fatal("an unset MinCurrentPct must mean the default floor, not none")
	}
}
