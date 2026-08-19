// Package anomaly flags usage that does not look like the rest of your usage.
//
// Two detectors, aimed at the two ways an agent session goes wrong without
// anyone noticing:
//
//   - Burn-rate spikes. A window whose consumption rate is far above your own
//     recent baseline. Scored with a median/MAD z-score rather than mean and
//     standard deviation: a single runaway session moves a mean enough to hide
//     itself, while the median barely shifts.
//
//   - Tool loops. An agent repeating the same short cycle of tool calls —
//     read, edit, read, edit — making no progress. Detected as a repeating
//     period of 1 to 4 calls over a recent tail of the call sequence.
//
// Both take a series and return findings; neither touches the network or the
// disk, so both are fully unit-testable.
package anomaly

import (
	"math"
	"sort"
	"time"
)

// Kind identifies which detector produced a finding.
type Kind string

const (
	KindRateSpike Kind = "rate_spike"
	KindToolLoop  Kind = "tool_loop"
)

// Finding is one flagged anomaly.
type Finding struct {
	Kind Kind `json:"kind"`
	// Subject is the gauge for a rate spike, or the session id for a loop.
	Subject string `json:"subject"`
	// Detail is a short human-readable explanation.
	Detail string `json:"detail"`
	// Score is the MAD z-score for a spike; the repeat count for a loop.
	Score float64 `json:"score"`
	// At is when the anomaly was observed.
	At time.Time `json:"at"`
}

// RatePoint is one observation of a consumption rate, in percent per hour.
type RatePoint struct {
	At   time.Time
	Rate float64
}

// Config tunes both detectors.
type Config struct {
	// ZThreshold is the MAD z-score above which a rate counts as a spike.
	// 3.5 is the conventional cutoff for modified z-scores.
	ZThreshold float64 `json:"z_threshold"`
	// MinSamples is the fewest history points needed before scoring. Below
	// this the baseline is not yet meaningful and nothing is reported.
	MinSamples int `json:"min_samples"`
	// LoopWindow is how many trailing tool calls the loop detector reads.
	LoopWindow int `json:"loop_window"`
	// LoopRepeats is how many times a cycle must repeat to count as a loop.
	LoopRepeats int `json:"loop_repeats"`
}

func DefaultConfig() Config {
	return Config{
		ZThreshold:  3.5,
		MinSamples:  8,
		LoopWindow:  24,
		LoopRepeats: 4,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.ZThreshold <= 0 {
		c.ZThreshold = d.ZThreshold
	}
	if c.MinSamples <= 0 {
		c.MinSamples = d.MinSamples
	}
	if c.LoopWindow <= 0 {
		c.LoopWindow = d.LoopWindow
	}
	if c.LoopRepeats <= 0 {
		c.LoopRepeats = d.LoopRepeats
	}
	return c
}

// median of a copy of xs. Caller's slice is left alone.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// MAD is the median absolute deviation from the median.
func MAD(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := median(xs)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - m)
	}
	return median(dev)
}

// The constant that puts a modified z-score on the same scale as a standard
// one for normally distributed data (0.6745 is the 0.75 quantile of N(0,1)).
const madScale = 0.6745

// ModifiedZ scores x against the baseline in xs.
//
// When the MAD is zero — a baseline with no spread at all, common early on —
// it falls back to the mean absolute deviation so a genuine outlier is still
// caught instead of dividing by zero. If that is also zero, every sample is
// identical and only a different value can be an outlier.
func ModifiedZ(xs []float64, x float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := median(xs)
	scale := MAD(xs)
	if scale == 0 {
		var sum float64
		for _, v := range xs {
			sum += math.Abs(v - m)
		}
		scale = sum / float64(len(xs)) * madScale
	}
	if scale == 0 {
		if x == m {
			return 0
		}
		return math.Inf(1)
	}
	return madScale * (x - m) / scale
}

// RateSpike scores the newest point against the ones before it.
//
// Only upward deviations are reported: a quiet hour is not an anomaly worth a
// notification.
func RateSpike(cfg Config, gauge string, history []RatePoint) (Finding, bool) {
	cfg = cfg.withDefaults()
	if len(history) < cfg.MinSamples+1 {
		return Finding{}, false
	}
	latest := history[len(history)-1]
	baseline := make([]float64, 0, len(history)-1)
	for _, p := range history[:len(history)-1] {
		baseline = append(baseline, p.Rate)
	}

	z := ModifiedZ(baseline, latest.Rate)
	if z < cfg.ZThreshold {
		return Finding{}, false
	}
	return Finding{
		Kind:    KindRateSpike,
		Subject: gauge,
		Detail: "burn rate " + trim(latest.Rate) + "%/h is far above the recent median " +
			trim(median(baseline)) + "%/h",
		Score: z,
		At:    latest.At,
	}, true
}

// ToolLoop looks for a short repeating cycle at the end of a tool-call
// sequence.
//
// Only the tail matters: a session that looped earlier and recovered is not
// stuck now. Periods are tried shortest first so a genuine 1-call hammer is
// not reported as a longer cycle that happens to contain it.
func ToolLoop(cfg Config, sessionID string, calls []string, at time.Time) (Finding, bool) {
	cfg = cfg.withDefaults()
	tail := calls
	if len(tail) > cfg.LoopWindow {
		tail = tail[len(tail)-cfg.LoopWindow:]
	}

	for period := 1; period <= 4; period++ {
		need := period * cfg.LoopRepeats
		if len(tail) < need {
			continue
		}
		window := tail[len(tail)-need:]
		if !repeatsWithPeriod(window, period) {
			continue
		}
		return Finding{
			Kind:    KindToolLoop,
			Subject: sessionID,
			Detail: "the same " + itoa(period) + "-call cycle repeated " +
				itoa(cfg.LoopRepeats) + " times: " + join(window[:period], " → "),
			Score: float64(cfg.LoopRepeats),
			At:    at,
		}, true
	}
	return Finding{}, false
}

// repeatsWithPeriod reports whether xs is the same period-length block over
// and over.
func repeatsWithPeriod(xs []string, period int) bool {
	if period <= 0 || len(xs) < period*2 {
		return false
	}
	for i := period; i < len(xs); i++ {
		if xs[i] != xs[i%period] {
			return false
		}
	}
	// A cycle whose elements are all identical is a period-1 loop; report it
	// as such rather than as a longer one, so callers see the shortest truth.
	if period > 1 && allSame(xs) {
		return false
	}
	return true
}

func allSame(xs []string) bool {
	for _, x := range xs[1:] {
		if x != xs[0] {
			return false
		}
	}
	return true
}

func join(xs []string, sep string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += sep
		}
		out += x
	}
	return out
}

// trim renders a rate with one decimal place.
func trim(v float64) string {
	whole := int(v)
	frac := int(math.Abs(v-float64(whole))*10 + 0.5)
	if frac == 10 {
		whole++
		frac = 0
	}
	return itoa(whole) + "." + itoa(frac)
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
