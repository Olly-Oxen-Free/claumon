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
	spans := spansFrom(stamps, actAt(3), false, cold5m)
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
	spans := spansFrom(stamps, actAt(14), false, cold5m)
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

func TestLongAbsenceStillBridgesTheLine(t *testing.T) {
	// Two hours of silence. The session did not stop existing — a transcript
	// gap says the context went cold, and only the process can say the session
	// ended. So the line must stay continuous, bridged cold.
	stamps := []time.Time{actAt(0), actAt(2), actAt(122), actAt(124)}
	spans := spansFrom(stamps, actAt(124), false, cold5m)
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3: %+v", len(spans), spans)
	}
	if spans[1].Kind != SpanIdle {
		t.Errorf("the gap must be bridged cold, not broken: %+v", spans)
	}
	// Contiguous end to end: no undrawn stretch anywhere.
	for i := 0; i < len(spans)-1; i++ {
		if !spans[i].To.Equal(spans[i+1].From) {
			t.Errorf("spans %d and %d are not contiguous: %+v", i, i+1, spans)
		}
	}
}

func TestRunningSessionStaysColdIndefinitely(t *testing.T) {
	// A live session quiet for a day is cold for that whole day — the dotting
	// runs until something happens or the session ends, with no threshold at
	// which it gives up and breaks.
	stamps := []time.Time{actAt(0), actAt(2)}
	spans := spansFrom(stamps, actAt(1440), true, cold1h)
	last := spans[len(spans)-1]
	if last.Kind != SpanIdle || !last.To.Equal(actAt(1440)) {
		t.Fatalf("cold tail must run to the end: %+v", spans)
	}
}

func TestOpenSessionGoneQuietGetsIdleTail(t *testing.T) {
	stamps := []time.Time{actAt(0), actAt(2)}
	spans := spansFrom(stamps, actAt(40), true, cold5m)
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
	spans := spansFrom(stamps, actAt(40), false, cold5m)
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1: %+v", len(spans), spans)
	}
}

func TestBriefQuietOpenSessionStaysSolid(t *testing.T) {
	// Under the cache TTL: still warm, so nothing is marked.
	stamps := []time.Time{actAt(0), actAt(2)}
	spans := spansFrom(stamps, actAt(4), true, cold5m)
	if len(spans) != 1 || spans[0].Kind != SpanActive {
		t.Fatalf("got %+v, want one active span", spans)
	}
}

func TestOutOfOrderStampsDoNotInvertSpans(t *testing.T) {
	// A split session replays its parent's history, so stamps can move
	// backwards. No span may end before it starts.
	stamps := []time.Time{actAt(0), actAt(50), actAt(10), actAt(52)}
	spans := spansFrom(stamps, actAt(52), false, cold5m)
	for _, s := range spans {
		if s.To.Before(s.From) {
			t.Fatalf("inverted span: %+v", s)
		}
	}
}

func TestNoStampsNoSpans(t *testing.T) {
	if spans := spansFrom(nil, actAt(10), true, cold5m); spans != nil {
		t.Fatalf("got %+v, want nil", spans)
	}
}

func TestColdThresholdFollowsTheSessionsTTL(t *testing.T) {
	// A ten-minute lull is cold on a 5-minute cache and still warm on an
	// hour-long one. The same stamps must read differently depending on which
	// TTL the session was actually using.
	stamps := []time.Time{actAt(0), actAt(2), actAt(12), actAt(14)}

	short := spansFrom(stamps, actAt(14), false, cold5m)
	if len(short) != 3 || short[1].Kind != SpanIdle {
		t.Fatalf("on a 5m cache this lull is cold: %+v", short)
	}

	long := spansFrom(stamps, actAt(14), false, cold1h)
	if len(long) != 1 || long[0].Kind != SpanActive {
		t.Fatalf("on a 1h cache this lull is still warm: %+v", long)
	}
}

func TestNonZeroAfter(t *testing.T) {
	cases := map[string]bool{
		"1385,": true,
		"0,":    false,
		"0}":    false,
		"00,":   false,
		"907}":  true,
		"":      false,
		"null,": false,
	}
	for in, want := range cases {
		if got := nonZeroAfter([]byte(in)); got != want {
			t.Errorf("nonZeroAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
