package anomaly

import (
	"math"
	"testing"
	"time"
)

func steadyHistory(rate float64, n int) []RatePoint {
	base := time.Unix(1_000_000, 0)
	out := make([]RatePoint, 0, n)
	for i := 0; i < n; i++ {
		// Small alternating wobble so MAD is non-zero, as real data is.
		r := rate
		if i%2 == 0 {
			r += 0.5
		} else {
			r -= 0.5
		}
		out = append(out, RatePoint{At: base.Add(time.Duration(i) * time.Hour), Rate: r})
	}
	return out
}

func TestSteadyUsageIsNotASpike(t *testing.T) {
	h := append(steadyHistory(5, 12), RatePoint{At: time.Unix(2_000_000, 0), Rate: 5.2})
	if f, ok := RateSpike(DefaultConfig(), "weekly", h); ok {
		t.Fatalf("steady usage flagged: %+v", f)
	}
}

func TestRunawayRateIsFlagged(t *testing.T) {
	h := append(steadyHistory(5, 12), RatePoint{At: time.Unix(2_000_000, 0), Rate: 60})
	f, ok := RateSpike(DefaultConfig(), "weekly", h)
	if !ok {
		t.Fatal("a 12x jump over the baseline should be flagged")
	}
	if f.Kind != KindRateSpike || f.Subject != "weekly" {
		t.Fatalf("unexpected finding: %+v", f)
	}
	if f.Score < DefaultConfig().ZThreshold {
		t.Fatalf("score %v below threshold", f.Score)
	}
}

func TestQuietPeriodIsNotAnAnomaly(t *testing.T) {
	h := append(steadyHistory(20, 12), RatePoint{At: time.Unix(2_000_000, 0), Rate: 0})
	if _, ok := RateSpike(DefaultConfig(), "weekly", h); ok {
		t.Fatal("a drop to zero must not be reported as a spike")
	}
}

func TestShortHistoryIsNotScored(t *testing.T) {
	h := append(steadyHistory(5, 3), RatePoint{At: time.Unix(2_000_000, 0), Rate: 90})
	if _, ok := RateSpike(DefaultConfig(), "weekly", h); ok {
		t.Fatal("must not score against a baseline that is too small to mean anything")
	}
}

func TestOneOutlierDoesNotHideTheNext(t *testing.T) {
	// A mean-based detector would be dragged up by the first spike enough to
	// miss the second. The median barely moves.
	h := steadyHistory(5, 12)
	h = append(h, RatePoint{At: time.Unix(1_900_000, 0), Rate: 80})
	h = append(h, RatePoint{At: time.Unix(2_000_000, 0), Rate: 75})
	if _, ok := RateSpike(DefaultConfig(), "weekly", h); !ok {
		t.Fatal("a second spike must still be caught after a first one")
	}
}

func TestModifiedZHandlesAZeroSpreadBaseline(t *testing.T) {
	flat := []float64{4, 4, 4, 4, 4, 4}
	if z := ModifiedZ(flat, 4); z != 0 {
		t.Fatalf("identical value in a flat baseline scored %v, want 0", z)
	}
	if z := ModifiedZ(flat, 40); !math.IsInf(z, 1) {
		t.Fatalf("an outlier against a flat baseline scored %v, want +Inf", z)
	}
}

func TestNoLoopInVariedWork(t *testing.T) {
	calls := []string{"Read", "Grep", "Edit", "Bash", "Read", "Write", "Bash", "Edit"}
	if f, ok := ToolLoop(DefaultConfig(), "s1", calls, time.Now()); ok {
		t.Fatalf("varied work flagged as a loop: %+v", f)
	}
}

func TestTwoCallCycleIsALoop(t *testing.T) {
	calls := []string{"Grep", "Bash"}
	for i := 0; i < 4; i++ {
		calls = append(calls, "Read", "Edit")
	}
	f, ok := ToolLoop(DefaultConfig(), "s1", calls, time.Now())
	if !ok {
		t.Fatal("read/edit repeated four times is a loop")
	}
	if f.Kind != KindToolLoop || f.Subject != "s1" {
		t.Fatalf("unexpected finding: %+v", f)
	}
	if want := "Read → Edit"; !contains(f.Detail, want) {
		t.Fatalf("detail %q missing %q", f.Detail, want)
	}
}

func TestRepeatedSingleCallIsAPeriodOneLoop(t *testing.T) {
	calls := []string{"Bash", "Bash", "Bash", "Bash"}
	f, ok := ToolLoop(DefaultConfig(), "s1", calls, time.Now())
	if !ok {
		t.Fatal("the same call four times running is a loop")
	}
	if !contains(f.Detail, "1-call cycle") {
		t.Fatalf("want the shortest period reported, got %q", f.Detail)
	}
}

func TestARecoveredLoopIsNotReported(t *testing.T) {
	// Looped earlier, then moved on: not stuck now.
	calls := []string{"Read", "Edit", "Read", "Edit", "Read", "Edit", "Read", "Edit"}
	calls = append(calls, "Bash", "Write", "Grep", "WebFetch")
	if _, ok := ToolLoop(DefaultConfig(), "s1", calls, time.Now()); ok {
		t.Fatal("only the tail should count")
	}
}

func TestTooFewCallsIsNotALoop(t *testing.T) {
	if _, ok := ToolLoop(DefaultConfig(), "s1", []string{"Read", "Edit"}, time.Now()); ok {
		t.Fatal("two calls cannot establish a repeat")
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
