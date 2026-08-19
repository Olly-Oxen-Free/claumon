package timeline

import (
	"testing"
	"time"
)

func actAt(mins int) time.Time {
	return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC).Add(time.Duration(mins) * time.Minute)
}

func TestContinuousWorkIsOneSpan(t *testing.T) {
	stamps := []time.Time{actAt(0), actAt(1), actAt(2), actAt(3)}
	spans := spansFrom(stamps, actAt(3), false)
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1: %+v", len(spans), spans)
	}
	if spans[0].Kind != SpanActive || !spans[0].From.Equal(actAt(0)) || !spans[0].To.Equal(actAt(3)) {
		t.Errorf("span = %+v", spans[0])
	}
}

func TestCacheColdLullIsBridgedDashed(t *testing.T) {
	// Ten minutes of nothing: past the cache TTL, well short of abandonment.
	// The line stays continuous but the lull is marked.
	stamps := []time.Time{actAt(0), actAt(2), actAt(12), actAt(14)}
	spans := spansFrom(stamps, actAt(14), false)
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3: %+v", len(spans), spans)
	}
	if spans[1].Kind != SpanIdle {
		t.Errorf("middle span kind = %q, want idle", spans[1].Kind)
	}
	// No gap in the drawn line: each span picks up where the last left off.
	if !spans[0].To.Equal(spans[1].From) || !spans[1].To.Equal(spans[2].From) {
		t.Errorf("spans are not contiguous: %+v", spans)
	}
}

func TestLongAbsenceBreaksTheLine(t *testing.T) {
	// An hour away: the session was put down and picked back up. The bar must
	// actually break, not bridge.
	stamps := []time.Time{actAt(0), actAt(2), actAt(62), actAt(64)}
	spans := spansFrom(stamps, actAt(64), false)
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(spans), spans)
	}
	for _, s := range spans {
		if s.Kind != SpanActive {
			t.Errorf("span %+v should be active; a break is drawn as absence, not as a span", s)
		}
	}
	if !spans[0].To.Equal(actAt(2)) || !spans[1].From.Equal(actAt(62)) {
		t.Errorf("break is in the wrong place: %+v", spans)
	}
}

func TestOpenSessionGoneQuietGetsIdleTail(t *testing.T) {
	stamps := []time.Time{actAt(0), actAt(2)}
	spans := spansFrom(stamps, actAt(40), true)
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(spans), spans)
	}
	tail := spans[1]
	if tail.Kind != SpanIdle || !tail.From.Equal(actAt(2)) || !tail.To.Equal(actAt(40)) {
		t.Errorf("tail = %+v, want idle from 2 to 40", tail)
	}
}

func TestClosedSessionGetsNoTail(t *testing.T) {
	// A session that has ended is not idle, it is over. Drawing a dashed tail
	// to now would claim it is still open.
	stamps := []time.Time{actAt(0), actAt(2)}
	spans := spansFrom(stamps, actAt(40), false)
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1: %+v", len(spans), spans)
	}
}

func TestBriefQuietOpenSessionStaysSolid(t *testing.T) {
	// Under the cache TTL: still warm, so nothing is marked.
	stamps := []time.Time{actAt(0), actAt(2)}
	spans := spansFrom(stamps, actAt(4), true)
	if len(spans) != 1 || spans[0].Kind != SpanActive {
		t.Fatalf("got %+v, want one active span", spans)
	}
}

func TestOutOfOrderStampsDoNotInvertSpans(t *testing.T) {
	// A split session replays its parent's history, so stamps can move
	// backwards. No span may end before it starts.
	stamps := []time.Time{actAt(0), actAt(50), actAt(10), actAt(52)}
	spans := spansFrom(stamps, actAt(52), false)
	for _, s := range spans {
		if s.To.Before(s.From) {
			t.Fatalf("inverted span: %+v", s)
		}
	}
}

func TestNoStampsNoSpans(t *testing.T) {
	if spans := spansFrom(nil, actAt(10), true); spans != nil {
		t.Fatalf("got %+v, want nil", spans)
	}
}
