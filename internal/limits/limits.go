// Package limits watches the shape of your rate-limit windows and reports when
// one resets, or when its schedule moves under you.
//
// This is a different question from "how much have I used", which the gauges
// answer, and from "where is this heading", which the forecast answers. A
// window can reset early, late, or be rescheduled by Anthropic; a new scoped
// limit can appear; an old one can vanish. Those events change what your
// budget actually is, and nothing else in claumon notices them.
//
// The detection rules are ported from nirvana-claude-watch, which ran as a
// separate daemon against the same endpoint. Its semantics are preserved
// deliberately, including the awkward ones — sub-minute jitter in resets_at is
// server noise rather than a reschedule, a first poll is always silent because
// everything would look new, and a threshold crossing fires once for the
// highest band cleared rather than once per band.
//
// The diff engine is pure: state in, events out, no I/O.
package limits

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Kind identifies what happened to a limit.
type Kind string

const (
	// KindReset: the window rolled over and a fresh budget started.
	KindReset Kind = "reset_reached"
	// KindScheduleChanged: resets_at moved while the window was still open.
	// This is the unscheduled change worth waking someone for.
	KindScheduleChanged Kind = "schedule_changed"
	// KindAppeared: a limit that was not previously reported.
	KindAppeared Kind = "limit_appeared"
	// KindVanished: a limit that stopped being reported.
	KindVanished Kind = "limit_vanished"
	// KindApproaching: usage crossed a configured threshold band.
	KindApproaching Kind = "approaching"
)

// Snapshot is one limit as the usage API reports it.
type Snapshot struct {
	Kind     string    `json:"kind"`
	Group    string    `json:"group,omitempty"`
	Percent  int64     `json:"percent"`
	Severity string    `json:"severity,omitempty"`
	ResetsAt time.Time `json:"resets_at"`
	// ScopeModel is the model display name for a scoped limit, e.g. "Fable".
	ScopeModel string `json:"scope_model,omitempty"`
	IsActive   bool   `json:"is_active,omitempty"`
}

// ID is the limit's stable identity: its kind, plus the scope's model name
// when it has one. Without the scope, the per-model weekly limit and the
// overall weekly limit would collide.
func (s Snapshot) ID() string {
	if s.ScopeModel != "" {
		return s.Kind + ":" + s.ScopeModel
	}
	return s.Kind
}

// Event is one detected change.
type Event struct {
	Kind  Kind   `json:"kind"`
	Limit string `json:"limit"`
	// Old and New are set on KindScheduleChanged.
	Old time.Time `json:"old,omitzero"`
	New time.Time `json:"new,omitzero"`
	// Threshold and Percent are set on KindApproaching.
	Threshold int       `json:"threshold,omitempty"`
	Percent   int64     `json:"percent,omitempty"`
	At        time.Time `json:"at"`
}

// wire mirrors one entry of the usage response's "limits" array.
type wire struct {
	Kind     string  `json:"kind"`
	Group    string  `json:"group"`
	Percent  int64   `json:"percent"`
	Severity string  `json:"severity"`
	ResetsAt *string `json:"resets_at"`
	IsActive bool    `json:"is_active"`
	Scope    *struct {
		Model *struct {
			DisplayName *string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// Parse pulls the limits array out of a raw usage response.
//
// Entries are decoded one at a time and a bad one is skipped rather than
// failing the whole poll: the API intermittently emits scoped limits with a
// null resets_at, and letting one of those blind the watcher costs hours of
// missed events.
func Parse(raw json.RawMessage) []Snapshot {
	var body struct {
		Limits []json.RawMessage `json:"limits"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	out := make([]Snapshot, 0, len(body.Limits))
	for _, entry := range body.Limits {
		var w wire
		if err := json.Unmarshal(entry, &w); err != nil {
			continue
		}
		if w.Kind == "" || w.ResetsAt == nil || *w.ResetsAt == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, *w.ResetsAt)
		if err != nil {
			continue
		}
		s := Snapshot{
			Kind:     w.Kind,
			Group:    w.Group,
			Percent:  w.Percent,
			Severity: w.Severity,
			ResetsAt: at.UTC(),
			IsActive: w.IsActive,
		}
		if w.Scope != nil && w.Scope.Model != nil && w.Scope.Model.DisplayName != nil {
			s.ScopeModel = *w.Scope.Model.DisplayName
		}
		out = append(out, s)
	}
	return out
}

// A resets_at move smaller than this is server jitter, not a reschedule.
const scheduleTolerance = time.Minute

// Diff compares the previous state against a fresh reading and returns the
// events worth reporting, updating the state in place.
//
// The first poll after a cold start is silent: with no prior reading every
// limit would look newly appeared, which is noise rather than news.
func Diff(state *State, fresh []Snapshot, now time.Time, thresholds []int) []Event {
	firstRun := len(state.Limits) == 0

	old := make(map[string]Snapshot, len(state.Limits))
	for _, l := range state.Limits {
		old[l.ID()] = l
	}
	freshIDs := make(map[string]Snapshot, len(fresh))
	for _, l := range fresh {
		freshIDs[l.ID()] = l
	}

	var events []Event
	if !firstRun {
		for _, limit := range fresh {
			id := limit.ID()
			prev, seen := old[id]
			if !seen {
				events = append(events, Event{Kind: KindAppeared, Limit: id, At: now})
				continue
			}

			moved := absDuration(limit.ResetsAt.Sub(prev.ResetsAt)) > scheduleTolerance
			switch {
			case moved && !prev.ResetsAt.After(now):
				// The old window's reset time has passed and a new one is in
				// place: the window rolled over. The exact-time timer may have
				// already announced it, so honour that dedupe.
				delete(state.NotifiedThresholds, id)
				if fired, ok := state.FiredResets[id]; ok && fired.Equal(prev.ResetsAt) {
					break
				}
				state.FiredResets[id] = prev.ResetsAt
				events = append(events, Event{Kind: KindReset, Limit: id, At: now})
			case moved:
				// Reset time moved while the window is still open. This is the
				// unscheduled change: the budget you planned around is gone.
				events = append(events, Event{
					Kind:  KindScheduleChanged,
					Limit: id,
					Old:   prev.ResetsAt,
					New:   limit.ResetsAt,
					At:    now,
				})
			default:
				// Same window. Notify once for the highest band crossed since
				// the last notification: emitting one event per band produced
				// duplicate popups ("past 80%" and "past 95%" in one poll).
				if ev, ok := crossing(state, id, limit.Percent, thresholds, now); ok {
					events = append(events, ev)
				}
			}
		}

		for id := range old {
			if _, ok := freshIDs[id]; !ok {
				events = append(events, Event{Kind: KindVanished, Limit: id, At: now})
			}
		}
	}

	// Keep the dedupe maps from growing without bound as limits come and go.
	for id := range state.FiredResets {
		if _, ok := freshIDs[id]; !ok {
			delete(state.FiredResets, id)
		}
	}
	for id := range state.NotifiedThresholds {
		if _, ok := freshIDs[id]; !ok {
			delete(state.NotifiedThresholds, id)
		}
	}

	state.Limits = append([]Snapshot(nil), fresh...)
	state.LastPoll = now

	// Deterministic order so a poll that produces several events reads the
	// same way every time.
	sort.SliceStable(events, func(i, j int) bool { return events[i].Limit < events[j].Limit })
	return events
}

// crossing returns an Approaching event for the highest threshold newly
// cleared, if any.
func crossing(state *State, id string, percent int64, thresholds []int, now time.Time) (Event, bool) {
	seen := state.NotifiedThresholds[id]
	best := 0
	for _, th := range thresholds {
		if th > seen && percent >= int64(th) && th > best {
			best = th
		}
	}
	if best == 0 {
		return Event{}, false
	}
	state.NotifiedThresholds[id] = best
	return Event{
		Kind:      KindApproaching,
		Limit:     id,
		Threshold: best,
		Percent:   percent,
		At:        now,
	}, true
}

// NextReset returns the soonest future reset across all limits, which is when
// a punctual announcement is due.
func NextReset(snaps []Snapshot, now time.Time) (string, time.Time, bool) {
	var bestID string
	var best time.Time
	for _, s := range snaps {
		if !s.ResetsAt.After(now) {
			continue
		}
		if best.IsZero() || s.ResetsAt.Before(best) {
			best, bestID = s.ResetsAt, s.ID()
		}
	}
	return bestID, best, !best.IsZero()
}

// MarkFired records that a reset was announced by the timer, so the poll that
// later observes the rollover stays quiet.
func (s *State) MarkFired(id string, resetsAt time.Time) bool {
	if s.FiredResets == nil {
		s.FiredResets = map[string]time.Time{}
	}
	if prev, ok := s.FiredResets[id]; ok && prev.Equal(resetsAt) {
		return false
	}
	s.FiredResets[id] = resetsAt
	delete(s.NotifiedThresholds, id)
	return true
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// friendly turns a limit id into the words a person uses for it.
func friendly(id string) string {
	switch id {
	case "session":
		return "session"
	case "weekly_all":
		return "weekly"
	}
	// "weekly_scoped:Fable" reads as just "Fable".
	for i := 0; i < len(id); i++ {
		if id[i] == ':' {
			return id[i+1:]
		}
	}
	return id
}

// Message is a rendered event, ready for a notification or an email.
type Message struct {
	Title string
	Body  string
	// Urgency is a notify-send level: low, normal, or critical.
	Urgency string
}

// Render turns an event into human words.
func Render(ev Event) Message {
	name := friendly(ev.Limit)
	switch ev.Kind {
	case KindReset:
		return Message{
			Title:   fmt.Sprintf("Claude %s limit reset", name),
			Body:    "New window started — full budget available.",
			Urgency: "normal",
		}
	case KindScheduleChanged:
		return Message{
			Title: fmt.Sprintf("Claude %s limit schedule changed", name),
			Body: fmt.Sprintf("Reset moved from %s to %s.",
				ev.Old.Format("2006-01-02 15:04 UTC"),
				ev.New.Format("2006-01-02 15:04 UTC")),
			Urgency: "critical",
		}
	case KindAppeared:
		return Message{
			Title:   fmt.Sprintf("New Claude limit: %s", name),
			Body:    fmt.Sprintf("Limit %q appeared on your account.", ev.Limit),
			Urgency: "normal",
		}
	case KindVanished:
		return Message{
			Title:   fmt.Sprintf("Claude limit removed: %s", name),
			Body:    fmt.Sprintf("Limit %q was removed — possibly extended or ended.", ev.Limit),
			Urgency: "critical",
		}
	case KindApproaching:
		urgency := "normal"
		if ev.Threshold >= 95 {
			urgency = "critical"
		}
		return Message{
			// Title names the band that triggered, body gives the live figure.
			// Mixing the two ("99% in the title, 80% in the body") reads as a bug.
			Title:   fmt.Sprintf("Claude %s usage past %d%%", name, ev.Threshold),
			Body:    fmt.Sprintf("Now at %d%% of the window budget.", ev.Percent),
			Urgency: urgency,
		}
	}
	return Message{Title: "Claude limit event", Body: string(ev.Kind), Urgency: "normal"}
}
