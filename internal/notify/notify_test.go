package notify

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig() Config {
	c := DefaultConfig()
	c.Enabled = true
	c.Default = Channels{Desktop: false, Email: true}
	c.Email = EmailConfig{
		To: "me@example.com", From: "claumon@example.com",
		SMTPHost: "127.0.0.1", SMTPPort: 1025, SMTPUser: "claumon@example.com",
		PasswordCommand: "echo  hunter2 ",
	}
	return c
}

// recorder captures what would have been sent.
type recorder struct {
	mu    sync.Mutex
	calls []string
	fail  int
	err   error
}

func (r *recorder) send(cfg EmailConfig, password, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, subject+"|"+body+"|"+password)
	if len(r.calls) <= r.fail {
		return r.err
	}
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestDisabledConfigSendsNothing(t *testing.T) {
	cfg := testConfig()
	cfg.Enabled = false
	if ch := cfg.ChannelsFor("reset_reached"); ch.Desktop || ch.Email {
		t.Fatalf("disabled config routed somewhere: %+v", ch)
	}
}

func TestPerEventRoutingOverridesTheDefault(t *testing.T) {
	cfg := testConfig()
	cfg.Default = Channels{Desktop: true}
	cfg.Events = map[string]Channels{
		"reset_reached": {Desktop: true, Email: true},
		"approaching":   {Desktop: true},
	}
	if ch := cfg.ChannelsFor("reset_reached"); !ch.Email {
		t.Fatal("a reset should be email-worthy when routed so")
	}
	if ch := cfg.ChannelsFor("approaching"); ch.Email {
		t.Fatal("a threshold crossing should stay off email here")
	}
	// Unlisted kinds fall back.
	if ch := cfg.ChannelsFor("limit_vanished"); !ch.Desktop || ch.Email {
		t.Fatalf("fallback wrong: %+v", ch)
	}
}

func TestEmailIsSentWithTheResolvedPassword(t *testing.T) {
	rec := &recorder{}
	n := New(testConfig())
	n.send = rec.send

	n.Notify("reset_reached", "Claude weekly limit reset", "New window started.", "normal")
	waitFor(t, func() bool { return rec.count() == 1 })

	got := rec.calls[0]
	if !strings.HasPrefix(got, "Claude weekly limit reset|New window started.|") {
		t.Fatalf("subject/body wrong: %q", got)
	}
	if !strings.HasSuffix(got, "|hunter2") {
		t.Fatalf("password not resolved and trimmed: %q", got)
	}
}

func TestEmailRetriesThenGivesUp(t *testing.T) {
	rec := &recorder{fail: 2, err: errors.New("bridge starting")}
	cfg := testConfig()
	n := New(cfg)
	n.send = rec.send

	n.Notify("reset_reached", "s", "b", "normal")
	// Two failures then success: the widening delay makes this take ~5s.
	waitFor2(t, 30*time.Second, func() bool { return rec.count() == 3 })
}

func waitFor2(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestMissingPasswordCommandIsReportedNotSent(t *testing.T) {
	cfg := testConfig()
	cfg.Email.PasswordCommand = ""
	rec := &recorder{}
	n := New(cfg)
	n.send = rec.send

	n.Notify("reset_reached", "s", "b", "normal")
	time.Sleep(200 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatal("must not attempt SMTP without a password")
	}
}

func TestResolvePasswordTrimsAndRejectsEmpty(t *testing.T) {
	pw, err := ResolvePassword(EmailConfig{PasswordCommand: "printf '  s3cret \n'"})
	if err != nil || pw != "s3cret" {
		t.Fatalf("pw=%q err=%v", pw, err)
	}
	if _, err := ResolvePassword(EmailConfig{PasswordCommand: "true"}); err == nil {
		t.Fatal("empty output must be an error, not an empty password")
	}
	if _, err := ResolvePassword(EmailConfig{PasswordCommand: "false"}); err == nil {
		t.Fatal("a failing command must be an error")
	}
	if _, err := ResolvePassword(EmailConfig{}); err == nil {
		t.Fatal("no command configured must be an error")
	}
}

func TestBuiltMessageHasTheRequiredHeaders(t *testing.T) {
	cfg := testConfig().Email
	msg := string(BuildMessage(cfg, "Claude weekly limit reset", "New window started."))
	for _, want := range []string{
		"From: claumon@example.com\r\n",
		"To: me@example.com\r\n",
		"Subject: Claude weekly limit reset\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\n\r\nNew window started.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n---\n%s", want, msg)
		}
	}
}

func TestHeadersAreSeparatedFromTheBody(t *testing.T) {
	msg := string(BuildMessage(testConfig().Email, "s", "line one\nline two"))
	i := strings.Index(msg, "\r\n\r\n")
	if i < 0 {
		t.Fatal("no header/body separator")
	}
	if strings.Contains(msg[:i], "line one") {
		t.Fatal("body leaked into the headers")
	}
}
