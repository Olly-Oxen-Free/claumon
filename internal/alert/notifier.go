package alert

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// Notifier delivers alerts and remembers what it has already said.
//
// Two independent brakes, because one is not enough:
//
//   - Dedupe per (gauge, window): one alert per window, plus one more if the
//     projection hardens from at-risk to likely. The window identity is
//     normalized to the minute — the API's raw resets_at jitters by fractions
//     of a second between polls, and keying on it made every poll look like a
//     new window, so this suppressed nothing at all.
//   - A minimum interval per gauge, which holds even across an escalation or a
//     window rollover. The dedupe answers "have I said this?"; the interval
//     answers "have I said anything, recently?" — and the second question is
//     the one that keeps a burst from reaching the user.
type Notifier struct {
	cfg    Config
	client *http.Client

	mu   sync.Mutex
	sent map[string]Level
	last map[string]time.Time
}

func NewNotifier(cfg Config) *Notifier {
	return &Notifier{
		cfg:    cfg.withDefaults(),
		client: &http.Client{Timeout: 10 * time.Second},
		sent:   make(map[string]Level),
		last:   make(map[string]time.Time),
	}
}

// Config returns the notifier's settings.
func (n *Notifier) Config() Config { return n.cfg }

// shouldSend reports whether this alert is new, and records it if so.
//
// An escalation (at-risk becoming likely) is worth a second notification; the
// reverse is not, because a projection easing back below the cap is exactly
// the outcome the first alert was asking for.
func (n *Notifier) shouldSend(a Alert, now time.Time) bool {
	key := a.Gauge + "|" + a.ResetAt
	n.mu.Lock()
	defer n.mu.Unlock()
	prev, seen := n.sent[key]
	if seen && !(prev == LevelAtRisk && a.Level == LevelLikely) {
		return false
	}
	// The rate limit is per gauge, not per window: a window rolling over is
	// not a reason to speak twice inside a minute.
	if gap := n.cfg.MinInterval(); gap > 0 {
		if at, ok := n.last[a.Gauge]; ok && now.Sub(at) < gap {
			return false
		}
	}
	n.sent[key] = a.Level
	n.last[a.Gauge] = now
	return true
}

// Consider evaluates a forecast and delivers the alert if one is warranted and
// has not been sent for this window. Returns the alert when it was delivered.
func (n *Notifier) Consider(f Forecast, now time.Time) (Alert, bool) {
	a, ok := Evaluate(n.cfg, f, now)
	if !ok || !n.shouldSend(a, now) {
		return Alert{}, false
	}
	n.deliver(a)
	return a, true
}

// Forget drops the dedupe record for a window. Used when a window's ResetAt
// changes so a rescheduled window is not silently suppressed.
func (n *Notifier) Forget(gauge, resetAt string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.sent, gauge+"|"+NormalizeWindow(resetAt))
}

// deliver fans the alert out to every configured channel. Delivery is
// best-effort and never blocks the caller's poll loop.
func (n *Notifier) deliver(a Alert) {
	log.Printf("[alert] %s: %s", a.Title, a.Body)
	if n.cfg.Desktop {
		go n.sendDesktop(a)
	}
	if n.cfg.WebhookURL != "" {
		go n.sendWebhook(a)
	}
}

// sendDesktop posts a notification through notify-send. Absent on macOS and
// Windows, where the log line remains the record.
func (n *Notifier) sendDesktop(a Alert) {
	if runtime.GOOS != "linux" {
		return
	}
	urgency := "normal"
	if a.Level == LevelLikely {
		urgency = "critical"
	}
	cmd := exec.Command("notify-send",
		"--app-name=claumon",
		"--urgency="+urgency,
		"--icon=utilities-system-monitor",
		a.Title, a.Body)
	if err := cmd.Run(); err != nil {
		log.Printf("[alert] notify-send failed: %v", err)
	}
}

func (n *Notifier) sendWebhook(a Alert) {
	payload, err := json.Marshal(a)
	if err != nil {
		log.Printf("[alert] webhook encode failed: %v", err)
		return
	}
	resp, err := n.client.Post(n.cfg.WebhookURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[alert] webhook POST failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[alert] webhook returned %s", resp.Status)
	}
}
