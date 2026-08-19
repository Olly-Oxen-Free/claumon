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

func TestProjectionBelowCapDoesNotAlert(t *testing.T) {
	f := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 82, Lower80Pct: 60, CurrentPct: 44}
	if _, ok := Evaluate(enabled(), f, time.Now()); ok {
		t.Fatal("projection under the cap must not alert")
	}
}

func TestCentralProjectionOverCapIsAtRisk(t *testing.T) {
	f := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 104, Lower80Pct: 88, CurrentPct: 44}
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
	f := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 200, Lower80Pct: 190}
	if _, ok := Evaluate(DefaultConfig(), f, time.Now()); ok {
		t.Fatal("default config is off and must stay silent")
	}
}

func TestGaugeFilterExcludesOtherGauges(t *testing.T) {
	c := enabled()
	c.Gauges = []string{"weekly"}
	f := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 200, Lower80Pct: 190}
	if _, ok := Evaluate(c, f, time.Now()); ok {
		t.Fatal("session alerts must be filtered out when only weekly is listed")
	}
}

func TestBodyCarriesTheNumbersAndETA(t *testing.T) {
	now := time.Date(2026, 8, 18, 20, 0, 0, 0, time.Local)
	f := Forecast{
		Gauge: "weekly", ResetAt: "r1",
		ProjectedPct: 112.4, Lower80Pct: 101, CurrentPct: 44.6,
		ETA: now.Add(3 * time.Hour),
	}
	a, ok := Evaluate(enabled(), f, now)
	if !ok {
		t.Fatal("expected an alert")
	}
	for _, want := range []string{"45%", "112%", "100%", "23:00"} {
		if !contains(a.Body, want) {
			t.Fatalf("body %q missing %q", a.Body, want)
		}
	}
}

func TestNotifierSendsOncePerWindow(t *testing.T) {
	n := NewNotifier(enabled())
	f := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 104, Lower80Pct: 90}
	if _, ok := n.Consider(f, time.Now()); !ok {
		t.Fatal("first alert should be delivered")
	}
	if _, ok := n.Consider(f, time.Now()); ok {
		t.Fatal("same window must not alert twice")
	}
}

func TestNotifierReAlertsWhenRiskHardens(t *testing.T) {
	n := NewNotifier(enabled())
	atRisk := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 104, Lower80Pct: 90}
	likely := Forecast{Gauge: "weekly", ResetAt: "r1", ProjectedPct: 130, Lower80Pct: 108}
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
	n := NewNotifier(enabled())
	first := Forecast{Gauge: "session", ResetAt: "r1", ProjectedPct: 104, Lower80Pct: 90}
	second := Forecast{Gauge: "session", ResetAt: "r2", ProjectedPct: 104, Lower80Pct: 90}
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
