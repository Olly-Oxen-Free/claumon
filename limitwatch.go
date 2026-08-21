package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fabioconcina/claumon/internal/limits"
	"github.com/fabioconcina/claumon/internal/notify"
)

// limitWatch reports when a usage window resets, or when its schedule moves.
//
// It replaces the nirvana-claude-watch daemon, which polled the same endpoint
// from a separate process purely because claumon did not do this. claumon
// already holds the poll, the credentials, and the history, so the watcher
// belongs here.
//
// Two paths produce a reset announcement. The poll notices a window that has
// rolled over, and a ticker notices the moment a known reset time passes. The
// ticker exists because polling every two minutes would report a reset up to
// two minutes late, and the point of the announcement is that the budget is
// available *now*. Whichever fires first records the window in FiredResets so
// the other stays quiet.
type limitWatch struct {
	enabled    bool
	thresholds []int
	statePath  string
	notifier   *notify.Notifier

	mu    sync.Mutex
	state *limits.State
	// events is a capped log of what has been announced, kept because the
	// NirvanaOS cockpit page shows a history and the daemon it replaces kept
	// one. claumon's own state file stays free of presentation concerns.
	events []loggedEvent
	// viewPath is where the cockpit-facing view is published.
	viewPath string
}

// loggedEvent is one announcement, in the shape the cockpit page reads.
type loggedEvent struct {
	At      time.Time `json:"at"`
	Limit   string    `json:"limit"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// How many announcements the cockpit history keeps. Matches the daemon's cap.
const eventLogCap = 200

// How often the punctual path checks whether a reset time has arrived. Fine
// enough that an announcement is never noticeably late, coarse enough to cost
// nothing.
const resetTickInterval = 15 * time.Second

func newLimitWatch(cfg Config) *limitWatch {
	statePath := filepath.Join(filepath.Dir(cfg.DBPath), "limits-state.json")
	w := &limitWatch{
		enabled:    cfg.LimitsEnabled,
		thresholds: cfg.LimitThresholds,
		statePath:  statePath,
		viewPath:   filepath.Join(filepath.Dir(cfg.DBPath), "cockpit-limits.json"),
		notifier:   notify.New(cfg.Notify),
		state:      limits.LoadState(statePath),
	}
	if w.enabled {
		ch := cfg.Notify.ChannelsFor(string(limits.KindReset))
		log.Printf("[limits] watching %d limits from last run — reset alerts on (desktop=%v email=%v)",
			len(w.state.Limits), ch.Desktop, ch.Email)
		// Publish immediately so the cockpit page has the restored windows to
		// show rather than sitting empty until the first poll lands.
		w.publish()
	}
	return w
}

// observe runs the diff for one poll and announces whatever changed.
func (w *limitWatch) observe(raw json.RawMessage, now time.Time) {
	if !w.enabled || len(raw) == 0 {
		return
	}
	fresh := limits.Parse(raw)
	if len(fresh) == 0 {
		// Every entry was unusable. Treating that as "all limits vanished"
		// would fire a storm of false alarms, so hold the previous baseline.
		log.Printf("[limits] usage response carried no usable limits; keeping the previous reading")
		return
	}

	w.mu.Lock()
	events := limits.Diff(w.state, fresh, now, w.thresholds)
	if err := w.state.Save(w.statePath); err != nil {
		log.Printf("[limits] could not persist state: %v", err)
	}
	w.mu.Unlock()

	for _, ev := range events {
		w.announce(ev)
	}
	w.publish()
}

// watchResets announces a reset the moment its time arrives, without waiting
// for the next poll.
func (w *limitWatch) watchResets(ctx context.Context) {
	if !w.enabled {
		return
	}
	ticker := time.NewTicker(resetTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			w.fireDueResets(now.UTC())
		}
	}
}

// fireDueResets announces every known window whose reset time has passed and
// which has not been announced yet.
func (w *limitWatch) fireDueResets(now time.Time) {
	w.mu.Lock()
	var due []limits.Event
	for _, snap := range w.state.Limits {
		if snap.ResetsAt.After(now) {
			continue
		}
		if !w.state.MarkFired(snap.ID(), snap.ResetsAt) {
			continue
		}
		due = append(due, limits.Event{Kind: limits.KindReset, Limit: snap.ID(), At: now})
	}
	if len(due) > 0 {
		if err := w.state.Save(w.statePath); err != nil {
			log.Printf("[limits] could not persist state: %v", err)
		}
	}
	w.mu.Unlock()

	for _, ev := range due {
		w.announce(ev)
	}
	if len(due) > 0 {
		w.publish()
	}
}

func (w *limitWatch) announce(ev limits.Event) {
	msg := limits.Render(ev)
	log.Printf("[limits] %s: %s — %s", ev.Kind, msg.Title, msg.Body)
	w.notifier.Notify(string(ev.Kind), msg.Title, msg.Body, msg.Urgency)

	w.mu.Lock()
	w.events = append(w.events, loggedEvent{
		At:      ev.At,
		Limit:   ev.Limit,
		Kind:    string(ev.Kind),
		Message: msg.Title + " — " + msg.Body,
	})
	if len(w.events) > eventLogCap {
		w.events = w.events[len(w.events)-eventLogCap:]
	}
	w.mu.Unlock()
}

// Events returns the announcement history, newest last.
func (w *limitWatch) Events() []loggedEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]loggedEvent(nil), w.events...)
}

// cockpitView is the shape the NirvanaOS cockpit Claude page reads. It mirrors
// the retired daemon's state file so that page keeps working, without making
// claumon's own state carry a presentation format.
type cockpitView struct {
	LastPoll time.Time      `json:"last_poll,omitzero"`
	Limits   []cockpitLimit `json:"limits"`
	Events   []loggedEvent  `json:"events"`
}

type cockpitLimit struct {
	Kind     string `json:"kind"`
	Percent  int64  `json:"percent"`
	ResetsAt string `json:"resets_at"`
	Scope    *struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope,omitempty"`
}

// publish writes the cockpit view. Best-effort: a failure here must never stop
// an alert from going out.
func (w *limitWatch) publish() {
	w.mu.Lock()
	// Emit an empty array rather than null for an untouched history: a reader
	// that indexes it should not have to special-case the first run.
	events := make([]loggedEvent, 0, len(w.events))
	events = append(events, w.events...)
	view := cockpitView{LastPoll: w.state.LastPoll, Events: events}
	view.Limits = make([]cockpitLimit, 0, len(w.state.Limits))
	for _, l := range w.state.Limits {
		cl := cockpitLimit{
			Kind:     l.Kind,
			Percent:  l.Percent,
			ResetsAt: l.ResetsAt.Format(time.RFC3339),
		}
		if l.ScopeModel != "" {
			cl.Scope = &struct {
				Model struct {
					DisplayName string `json:"display_name"`
				} `json:"model"`
			}{}
			cl.Scope.Model.DisplayName = l.ScopeModel
		}
		view.Limits = append(view.Limits, cl)
	}
	path := w.viewPath
	w.mu.Unlock()

	data, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("[limits] could not write cockpit view: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[limits] could not publish cockpit view: %v", err)
	}
}

// Snapshot returns the last reading, for the API.
func (w *limitWatch) Snapshot() []limits.Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]limits.Snapshot(nil), w.state.Limits...)
}

// Test sends one message through every configured channel, so a new email
// setup can be proven without waiting for a real reset.
func (w *limitWatch) Test(kind string) {
	msg := limits.Render(limits.Event{Kind: limits.KindReset, Limit: "session"})
	msg.Title = "claumon test notification"
	msg.Body = "If you are reading this, delivery works."
	w.notifier.Notify(kind, msg.Title, msg.Body, "normal")
}

// runNotifyTest sends one message through the configured channels and reports
// what happened.
//
// Email delivery cannot be verified from the config alone — the bridge has to
// be running, the password command has to resolve, the account has to accept
// the sender. This proves the whole path without waiting for a real reset,
// which could be hours away.
func runNotifyTest() {
	cfg := loadConfig()
	kind := string(limits.KindReset)
	if len(os.Args) > 2 {
		kind = os.Args[2]
	}
	ch := cfg.Notify.ChannelsFor(kind)

	fmt.Printf("notifications: enabled=%v\n", cfg.Notify.Enabled)
	fmt.Printf("event kind:    %s\n", kind)
	fmt.Printf("routed to:     desktop=%v email=%v\n", ch.Desktop, ch.Email)
	if !cfg.Notify.Enabled {
		fmt.Println("\nNothing sent: set \"notify\": {\"enabled\": true} in ~/.claumon/config.json")
		return
	}
	if ch.Email {
		fmt.Printf("smtp:          %s:%d as %s -> %s\n",
			cfg.Notify.Email.SMTPHost, cfg.Notify.Email.SMTPPort,
			cfg.Notify.Email.SMTPUser, cfg.Notify.Email.To)
		if _, err := notify.ResolvePassword(cfg.Notify.Email); err != nil {
			fmt.Printf("password:      FAILED — %v\n", err)
		} else {
			fmt.Println("password:      resolved")
		}
	}

	n := notify.New(cfg.Notify)
	n.Notify(kind, "claumon test notification",
		"If you are reading this, delivery works.", "normal")

	// Notify is asynchronous by design so a stalled channel cannot block the
	// poller; wait here so the command does not exit before delivery.
	fmt.Println("\nsending…")
	time.Sleep(20 * time.Second)
	fmt.Println("done — check your desktop and inbox. Failures are logged above.")
}
