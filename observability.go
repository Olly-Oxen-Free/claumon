package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/fabioconcina/claumon/internal/alert"
	"github.com/fabioconcina/claumon/internal/anomaly"
	"github.com/fabioconcina/claumon/internal/forecast"
	"github.com/fabioconcina/claumon/internal/live"
	"github.com/fabioconcina/claumon/internal/otel"
	"github.com/fabioconcina/claumon/internal/parser"
)

// observability wires the forecast-risk alerter, the anomaly detectors, the
// live-session reader, and the OTLP exporter into the poll loop.
//
// It owns the small amount of state those four need to share: the burn-rate
// history the spike detector scores against, and the ring of recent findings
// the dashboard reads. Everything here is best-effort — a failure in any of it
// logs and returns, because none of it is worth interrupting usage polling for.
type observability struct {
	alerts   *alert.Notifier
	anomCfg  anomaly.Config
	anomOn   bool
	exporter *otel.Exporter
	reader   *live.Reader

	mu sync.Mutex
	// rates holds recent burn rates per gauge, oldest first.
	rates map[string][]anomaly.RatePoint
	// findings is a bounded ring of the most recent anomalies.
	findings []anomaly.Finding
	// loopSeen dedupes tool-loop findings per session, so one stuck agent
	// does not refill the ring on every file change.
	loopSeen map[string]string
}

// How much rate history to keep per gauge. Enough for a stable median without
// letting last week's habits define today's baseline.
const rateHistoryLen = 48

// How many findings the dashboard can see at once.
const findingsLen = 50

func newObservability(cfg Config) *observability {
	o := &observability{
		alerts:   alert.NewNotifier(cfg.Alerts),
		anomCfg:  cfg.Anomaly,
		anomOn:   cfg.AnomalyEnabled,
		exporter: otel.New(cfg.OTel),
		reader:   live.NewReader(cfg.ClaudeDir),
		rates:    make(map[string][]anomaly.RatePoint),
		loopSeen: make(map[string]string),
	}
	if cfg.Alerts.Enabled {
		log.Printf("[alert] forecast-risk alerts on (cap %.0f%%)", o.alerts.Config().CapPct)
	}
	if o.exporter.Enabled() {
		log.Printf("[otel] exporting to %s every %v",
			o.exporter.Config().Endpoint, o.exporter.Config().Interval())
	}
	return o
}

// LiveSessions is the provider handed to the HTTP layer.
func (o *observability) LiveSessions() []live.Session {
	if o.reader == nil {
		return nil
	}
	return o.reader.Sessions()
}

// Findings returns the recent anomaly ring, newest first.
func (o *observability) Findings() []anomaly.Finding {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]anomaly.Finding, len(o.findings))
	for i, f := range o.findings {
		out[len(o.findings)-1-i] = f
	}
	return out
}

func (o *observability) addFinding(f anomaly.Finding) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.findings = append(o.findings, f)
	if len(o.findings) > findingsLen {
		o.findings = o.findings[len(o.findings)-findingsLen:]
	}
}

// recordRate appends a burn rate and scores it against the gauge's history.
func (o *observability) recordRate(gauge string, rate float64, at time.Time) (anomaly.Finding, bool) {
	o.mu.Lock()
	hist := append(o.rates[gauge], anomaly.RatePoint{At: at, Rate: rate})
	if len(hist) > rateHistoryLen {
		hist = hist[len(hist)-rateHistoryLen:]
	}
	o.rates[gauge] = hist
	scored := append([]anomaly.RatePoint(nil), hist...)
	o.mu.Unlock()

	if !o.anomOn {
		return anomaly.Finding{}, false
	}
	return anomaly.RateSpike(o.anomCfg, gauge, scored)
}

// gaugeInput is one gauge's numbers for a single poll.
type gaugeInput struct {
	name       string
	resetAt    string
	currentPct float64
	payload    forecast.Payload
	hasPayload bool
}

// onUsage runs after each successful usage poll: alerts on forecast risk,
// scores the burn rate for spikes, and pushes metrics.
//
// `evt` is the SSE payload already built for the dashboard; forecasts were
// attached to it by attachForecasts, so this reads them rather than recomputing.
func (o *observability) onUsage(ctx context.Context, evt map[string]interface{}, now time.Time) {
	gauges := gaugesFromEvent(evt)

	for _, g := range gauges {
		if !g.hasPayload {
			continue
		}
		// The forecast's own rate estimate is the burn rate; it is already
		// smoothed over the recency window, which is what a baseline wants.
		if f, ok := o.recordRate(g.name, g.payload.RatePerHour, now); ok {
			log.Printf("[anomaly] %s: %s", f.Subject, f.Detail)
			o.addFinding(f)
		}

		o.alerts.Consider(alert.Forecast{
			Gauge:        g.name,
			ResetAt:      g.resetAt,
			ProjectedPct: g.payload.ProjectedPct,
			Lower80Pct:   g.payload.Lower80Pct,
			CurrentPct:   g.currentPct,
			ETA:          firstETA(g.payload),
		}, now)
	}

	o.export(ctx, gauges, now)
}

// firstETA picks the earliest threshold crossing the model produced, which is
// the one the user will hit first.
func firstETA(p forecast.Payload) time.Time {
	var best time.Time
	for _, e := range p.ETAs {
		if e.Median == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Median)
		if err != nil {
			continue
		}
		if best.IsZero() || t.Before(best) {
			best = t
		}
	}
	return best
}

// gaugesFromEvent pulls the per-gauge numbers back out of the SSE payload.
func gaugesFromEvent(evt map[string]interface{}) []gaugeInput {
	forecasts, _ := evt["forecast"].(map[string]forecast.Payload)

	out := []gaugeInput{
		{name: "session", resetAt: str(evt["session_reset_at"]), currentPct: num(evt["session_pct"])},
		{name: "weekly", resetAt: str(evt["weekly_reset_at"]), currentPct: num(evt["weekly_pct"])},
	}
	for i := range out {
		if p, ok := forecasts[out[i].name]; ok {
			out[i].payload = p
			out[i].hasPayload = true
		}
	}
	return out
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}

func num(v interface{}) float64 {
	f, _ := v.(float64)
	return f
}

// export pushes the current numbers to the collector.
func (o *observability) export(ctx context.Context, gauges []gaugeInput, now time.Time) {
	if !o.exporter.Enabled() {
		return
	}
	metrics := make([]otel.Metric, 0, len(gauges)*3+4)

	for _, g := range gauges {
		attrs := map[string]string{"window": g.name}
		metrics = append(metrics, otel.Metric{
			Name:        "claumon.usage.utilization",
			Description: "Rate-limit utilization for the window, as reported by the usage API",
			Unit:        "%",
			Value:       g.currentPct,
			Attributes:  attrs,
		})
		if !g.hasPayload {
			continue
		}
		metrics = append(metrics,
			otel.Metric{
				Name:        "claumon.forecast.projected_utilization",
				Description: "Projected utilization at reset",
				Unit:        "%",
				Value:       g.payload.ProjectedPct,
				Attributes:  attrs,
			},
			otel.Metric{
				Name:        "claumon.forecast.burn_rate",
				Description: "Estimated utilization consumed per hour",
				Unit:        "%/h",
				Value:       g.payload.RatePerHour,
				Attributes:  attrs,
			},
		)
	}

	// Live session counts, one series per status, so a Grafana panel can show
	// "waiting on me" without post-processing.
	counts := map[live.Status]int{
		live.StatusWorking: 0,
		live.StatusWaiting: 0,
		live.StatusIdle:    0,
		live.StatusDone:    0,
	}
	for _, s := range o.LiveSessions() {
		counts[s.Status]++
	}
	for status, n := range counts {
		metrics = append(metrics, otel.Metric{
			Name:        "claumon.sessions.live",
			Description: "Claude Code sessions currently in each state",
			Unit:        "1",
			Value:       float64(n),
			Attributes:  map[string]string{"status": string(status)},
		})
	}

	if err := o.exporter.Export(ctx, otel.Snapshot{At: now, Metrics: metrics}); err != nil {
		log.Printf("[otel] export failed: %v", err)
	}
}

// scanSessionsForLoops checks each session's recent tool calls for a repeating
// cycle. Called when a transcript changes, which is exactly when a loop would
// have advanced.
func (o *observability) scanSessionsForLoops(claudeDir string, now time.Time) {
	if !o.anomOn {
		return
	}
	sessions, err := parser.DiscoverTodaySessions(claudeDir)
	if err != nil {
		return
	}
	for _, s := range sessions {
		if s == nil {
			continue
		}
		calls, err := toolCallSequence(claudeDir, s.ID)
		if err != nil || len(calls) == 0 {
			continue
		}
		f, ok := anomaly.ToolLoop(o.anomCfg, s.ID, calls, now)
		if !ok {
			continue
		}
		// The same loop stays detected on every subsequent change; report it
		// once per distinct cycle.
		o.mu.Lock()
		already := o.loopSeen[s.ID] == f.Detail
		o.loopSeen[s.ID] = f.Detail
		o.mu.Unlock()
		if already {
			continue
		}
		log.Printf("[anomaly] session %s: %s", shortID(s.ID), f.Detail)
		o.addFinding(f)
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// toolCallSequence returns the session's tool calls in order, each identified
// by tool name plus arguments.
//
// The arguments are part of the identity because the detector must not treat a
// run of distinct Bash commands as a loop; only the same command repeated is
// one. Only the tail matters to the detector, but a transcript has to be read
// from the start to be parsed at all; sessions are capped at a day's work here
// (DiscoverTodaySessions), so this stays cheap.
func toolCallSequence(claudeDir, sessionID string) ([]anomaly.Call, error) {
	path := parser.FindSessionFile(claudeDir, sessionID)
	if path == "" {
		return nil, nil
	}
	messages, err := parser.ParseSessionDetail(path)
	if err != nil {
		return nil, err
	}
	var calls []anomaly.Call
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if tc.Name != "" {
				calls = append(calls, anomaly.NewCall(tc.Name, string(tc.Input)))
			}
		}
	}
	return calls, nil
}
