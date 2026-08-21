// Package alert raises a warning when the forecast says a usage window is
// heading past a cap before it resets.
//
// This deliberately does NOT alert on the current percentage. Threshold
// alerting ("you are at 80%") is already handled elsewhere on this machine by
// nirvana-claude-watch, which polls the same API and owns desktop and email
// delivery for reset, schedule-change, and threshold events. Duplicating it
// here would produce two popups for one fact.
//
// What claumon knows that a threshold watcher cannot is the projection: the
// forecast model estimates where utilization lands at reset, with an 80%
// credible interval. An alert fires when that projection clears the cap —
// which happens while current usage is still comfortably below it, and that
// lead time is the whole point.
package alert

import (
	"time"
)

// canonicalResetLayout is minute precision: the API returns resets_at with
// sub-second jitter that differs on every poll, and a window's identity must
// not change just because the clock did.
const canonicalResetLayout = "2006-01-02T15:04:00Z"

// NormalizeWindow reduces a resets_at string to a stable window identity.
//
// The API returns the same window as "…T19:20:00.310465+00:00" on one poll and
// "…T19:19:59.866966+00:00" on the next. Used raw as a dedupe key, every poll
// is a new window and every poll notifies — which is exactly what happened.
func NormalizeWindow(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Add(30 * time.Second).Truncate(time.Minute).Format(canonicalResetLayout)
}

// Level ranks how confident the projection is that the cap will be breached.
type Level string

const (
	// LevelAtRisk: the central projection clears the cap, but the lower bound
	// of the interval does not. Plausible, not settled.
	LevelAtRisk Level = "at_risk"
	// LevelLikely: even the lower bound of the 80% interval clears the cap.
	LevelLikely Level = "likely"
)

// Forecast is the subset of a gauge's forecast this package reasons about.
// Percentages are 0-100.
type Forecast struct {
	// Gauge is "session" or "weekly".
	Gauge string
	// ResetAt identifies the window. Two alerts for the same gauge and
	// ResetAt are the same alert.
	ResetAt string
	// ProjectedPct is the central estimate of utilization at reset.
	ProjectedPct float64
	// Lower80Pct is the lower bound of the 80% credible interval.
	Lower80Pct float64
	// CurrentPct is utilization right now, carried for the message text.
	CurrentPct float64
	// ETA is when the cap is first expected to be crossed, if the model
	// produced one. Zero when it did not.
	ETA time.Time
}

// Alert is one raised warning, ready for delivery.
type Alert struct {
	Gauge   string    `json:"gauge"`
	ResetAt string    `json:"reset_at"`
	Level   Level     `json:"level"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	ETA     time.Time `json:"eta,omitzero"`
	// ProjectedPct and CurrentPct are carried so a webhook receiver can act
	// on the numbers rather than parsing the body text.
	ProjectedPct float64   `json:"projected_pct"`
	CurrentPct   float64   `json:"current_pct"`
	RaisedAt     time.Time `json:"raised_at"`
}

// Config controls when an alert fires and where it goes.
type Config struct {
	// Enabled gates the whole feature. Off by default: a fresh install
	// should not start emitting desktop popups unasked.
	Enabled bool `json:"enabled"`
	// CapPct is the utilization the projection must clear, 0-100.
	CapPct float64 `json:"cap_pct"`
	// Desktop sends a notification through the session's notification
	// daemon.
	Desktop bool `json:"desktop"`
	// WebhookURL receives the alert as a JSON POST when non-empty.
	WebhookURL string `json:"webhook_url"`
	// Gauges limits which gauges can alert. Empty means all of them.
	Gauges []string `json:"gauges,omitempty"`

	// MinCurrentPct is how far into the window usage must be before a
	// projection is allowed to speak.
	//
	// Early in a window the model is extrapolating from very little: at 7%
	// used, a short burst projects to 300% and the projection swings by a
	// hundred points between polls. Those are real model outputs, but they are
	// not yet news — the window has hours of slack and the estimate has not
	// settled. Waiting until usage is materially along makes the warning both
	// stabler and actionable.
	MinCurrentPct float64 `json:"min_current_pct"`

	// MinIntervalMins is the shortest gap between two notifications for the
	// same gauge, whatever changed in between.
	//
	// Without it, an escalation or a re-opened window can still produce a
	// burst. The projection is a slow-moving quantity; being told about it
	// more than once an hour cannot change what the reader does.
	MinIntervalMins int `json:"min_interval_mins"`
}

// DefaultConfig is off, and — when switched on — warns about the cap the
// gauges actually enforce.
func DefaultConfig() Config {
	return Config{
		Enabled:         false,
		CapPct:          100,
		Desktop:         true,
		MinCurrentPct:   50,
		MinIntervalMins: 60,
	}
}

func (c Config) withDefaults() Config {
	if c.CapPct <= 0 {
		c.CapPct = 100
	}
	// Zero means "not configured", not "no floor": a config written before
	// these fields existed must get the paced behaviour, not the old
	// every-poll one. Set the field negative to genuinely disable a floor.
	if c.MinCurrentPct == 0 {
		c.MinCurrentPct = DefaultConfig().MinCurrentPct
	}
	if c.MinIntervalMins == 0 {
		c.MinIntervalMins = DefaultConfig().MinIntervalMins
	}
	return c
}

// MinInterval is the configured gap as a duration.
func (c Config) MinInterval() time.Duration {
	if c.MinIntervalMins < 0 {
		return 0
	}
	return time.Duration(c.MinIntervalMins) * time.Minute
}

func (c Config) allows(gauge string) bool {
	if len(c.Gauges) == 0 {
		return true
	}
	for _, g := range c.Gauges {
		if g == gauge {
			return true
		}
	}
	return false
}

// Evaluate decides whether a forecast warrants an alert, and at what level.
//
// Returns false when the projection is under the cap, when the gauge is
// filtered out, or when the feature is off. The caller is responsible for
// suppressing repeats; see Notifier.
func Evaluate(cfg Config, f Forecast, now time.Time) (Alert, bool) {
	cfg = cfg.withDefaults()
	if !cfg.Enabled || !cfg.allows(f.Gauge) {
		return Alert{}, false
	}
	if f.ProjectedPct < cfg.CapPct {
		return Alert{}, false
	}
	// Too early in the window for the projection to mean anything yet.
	if f.CurrentPct < cfg.MinCurrentPct {
		return Alert{}, false
	}

	level := LevelAtRisk
	if f.Lower80Pct >= cfg.CapPct {
		level = LevelLikely
	}

	return Alert{
		Gauge:        f.Gauge,
		ResetAt:      NormalizeWindow(f.ResetAt),
		Level:        level,
		Title:        title(f.Gauge, level),
		Body:         body(f, cfg.CapPct, level, now),
		ETA:          f.ETA,
		ProjectedPct: f.ProjectedPct,
		CurrentPct:   f.CurrentPct,
		RaisedAt:     now,
	}, true
}

func title(gauge string, level Level) string {
	window := "Session"
	if gauge == "weekly" {
		window = "Weekly"
	}
	if level == LevelLikely {
		return window + " limit will be reached"
	}
	return window + " limit at risk"
}

func body(f Forecast, cap float64, level Level, now time.Time) string {
	verb := "may reach"
	if level == LevelLikely {
		verb = "is on track to reach"
	}
	s := fmtPct(f.CurrentPct) + " now, " + verb + " " + fmtPct(f.ProjectedPct) +
		" by reset (cap " + fmtPct(cap) + ")."
	if !f.ETA.IsZero() && f.ETA.After(now) {
		s += " Expected around " + f.ETA.Local().Format("15:04") + "."
	}
	return s
}

// fmtPct renders a percentage with no decimals — an alert is a prompt to act,
// not a readout.
func fmtPct(v float64) string {
	return itoa(int(v+0.5)) + "%"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
