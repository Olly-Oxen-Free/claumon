package main

import (
	"context"
	"testing"
	"time"

	"github.com/fabioconcina/claumon/internal/anomaly"
	"github.com/fabioconcina/claumon/internal/forecast"
)

func anomalyFinding(i int) anomaly.Finding {
	return anomaly.Finding{
		Kind:    anomaly.KindRateSpike,
		Subject: "s" + itoaTest(i),
		At:      time.Now(),
	}
}

func itoaTest(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func testConfig() Config {
	cfg := defaultConfig()
	cfg.Alerts.Enabled = true
	cfg.Alerts.Desktop = false
	cfg.AnomalyEnabled = true
	return cfg
}

func TestGaugesAreReadBackOutOfTheEvent(t *testing.T) {
	evt := map[string]interface{}{
		"session_pct":      38.0,
		"weekly_pct":       44.0,
		"session_reset_at": "2026-08-19T04:19:59Z",
		"weekly_reset_at":  "2026-08-23T17:59:59Z",
		"forecast": map[string]forecast.Payload{
			"weekly": {ProjectedPct: 91, Lower80Pct: 80, RatePerHour: 2.5},
		},
	}
	got := gaugesFromEvent(evt)
	if len(got) != 2 {
		t.Fatalf("want session and weekly, got %d", len(got))
	}
	if got[0].name != "session" || got[0].currentPct != 38 {
		t.Fatalf("session gauge wrong: %+v", got[0])
	}
	if got[0].hasPayload {
		t.Fatal("session had no forecast in the event and must not claim one")
	}
	if !got[1].hasPayload || got[1].payload.ProjectedPct != 91 {
		t.Fatalf("weekly forecast not carried: %+v", got[1])
	}
	if got[1].resetAt != "2026-08-23T17:59:59Z" {
		t.Fatalf("reset window not carried: %q", got[1].resetAt)
	}
}

func TestEventWithoutForecastsIsHandled(t *testing.T) {
	// The first polls of a fresh install have no forecast attached.
	got := gaugesFromEvent(map[string]interface{}{"session_pct": 5.0})
	for _, g := range got {
		if g.hasPayload {
			t.Fatalf("%s claimed a forecast that was not there", g.name)
		}
	}
}

func TestFirstETAPicksTheEarliestCrossing(t *testing.T) {
	p := forecast.Payload{ETAs: []forecast.ETAPayload{
		{ThresholdPct: 100, Median: "2026-08-20T12:00:00Z"},
		{ThresholdPct: 80, Median: "2026-08-19T09:00:00Z"},
		{ThresholdPct: 90, Median: ""},
	}}
	got := firstETA(p)
	want, _ := time.Parse(time.RFC3339, "2026-08-19T09:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("eta = %v, want %v", got, want)
	}
}

func TestNoETAsYieldsTheZeroTime(t *testing.T) {
	if got := firstETA(forecast.Payload{}); !got.IsZero() {
		t.Fatalf("eta = %v, want zero", got)
	}
}

func TestBurnRateHistoryIsBounded(t *testing.T) {
	o := newObservability(testConfig())
	now := time.Now()
	for i := 0; i < rateHistoryLen*3; i++ {
		o.recordRate("weekly", 5, now.Add(time.Duration(i)*time.Minute))
	}
	o.mu.Lock()
	n := len(o.rates["weekly"])
	o.mu.Unlock()
	if n != rateHistoryLen {
		t.Fatalf("history length = %d, want capped at %d", n, rateHistoryLen)
	}
}

func TestSpikeIsDetectedThroughTheRecorder(t *testing.T) {
	o := newObservability(testConfig())
	now := time.Now()
	for i := 0; i < 20; i++ {
		rate := 5.0
		if i%2 == 0 {
			rate = 5.5
		}
		if _, ok := o.recordRate("weekly", rate, now); ok {
			t.Fatalf("steady rate flagged at sample %d", i)
		}
	}
	if _, ok := o.recordRate("weekly", 90, now); !ok {
		t.Fatal("a runaway rate should be flagged")
	}
}

func TestAnomalyDetectionOffMeansNoFindings(t *testing.T) {
	cfg := testConfig()
	cfg.AnomalyEnabled = false
	o := newObservability(cfg)
	now := time.Now()
	for i := 0; i < 20; i++ {
		o.recordRate("weekly", 5, now)
	}
	if _, ok := o.recordRate("weekly", 900, now); ok {
		t.Fatal("detection is off; nothing should be reported")
	}
}

func TestFindingsRingIsBoundedAndNewestFirst(t *testing.T) {
	o := newObservability(testConfig())
	for i := 0; i < findingsLen+10; i++ {
		o.addFinding(anomalyFinding(i))
	}
	got := o.Findings()
	if len(got) != findingsLen {
		t.Fatalf("ring holds %d, want %d", len(got), findingsLen)
	}
	if got[0].Subject != "s59" {
		t.Fatalf("newest finding = %q, want the most recent one first", got[0].Subject)
	}
}

func TestLiveSessionsIsEmptyWithoutHooks(t *testing.T) {
	cfg := testConfig()
	cfg.ClaudeDir = t.TempDir()
	o := newObservability(cfg)
	if got := o.LiveSessions(); len(got) != 0 {
		t.Fatalf("want no sessions without hooks, got %d", len(got))
	}
}

func TestExportIsANoOpWhenDisabled(t *testing.T) {
	o := newObservability(testConfig())
	// The default endpoint points at a collector that is not running; with
	// export disabled this must not even try, so it returns promptly.
	done := make(chan struct{})
	go func() {
		o.export(context.Background(), gaugesFromEvent(map[string]interface{}{}), time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled exporter attempted network I/O")
	}
}
