// Package notify delivers a message to the channels configured for it.
//
// Two channels: the desktop notification daemon, and email. Email matters
// because the events worth sending are the ones you want while away from the
// machine — a limit window resetting, or its schedule moving under you.
//
// Routing is per event kind, so a reset can be email-worthy while a threshold
// crossing stays on the desktop. That matrix is carried over from
// nirvana-claude-watch, whose job this package takes over.
package notify

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Channels says where one kind of event goes.
type Channels struct {
	Desktop bool `json:"desktop"`
	Email   bool `json:"email"`
}

// EmailConfig describes the SMTP account used for delivery.
//
// The defaults target a local Proton Mail Bridge, which listens on loopback
// and presents a self-signed certificate — hence the explicit allowance below.
type EmailConfig struct {
	To       string `json:"to"`
	From     string `json:"from"`
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	SMTPUser string `json:"smtp_user"`
	// PasswordCommand is a shell command whose trimmed stdout is the password.
	// A command rather than a literal so the secret can stay in a keyring
	// instead of a config file.
	PasswordCommand string `json:"password_command"`
}

// Config is the whole notification setup.
type Config struct {
	// Enabled gates every channel at once.
	Enabled bool `json:"enabled"`
	// Default applies to any event kind not named in Events.
	Default Channels `json:"default"`
	// Events routes specific kinds, keyed by the event kind string.
	Events map[string]Channels `json:"events,omitempty"`
	Email  EmailConfig         `json:"email"`
}

func DefaultConfig() Config {
	return Config{
		Enabled: false,
		Default: Channels{Desktop: true},
		Email: EmailConfig{
			SMTPHost: "127.0.0.1",
			SMTPPort: 1025,
		},
	}
}

// ChannelsFor resolves where an event kind should go.
func (c Config) ChannelsFor(kind string) Channels {
	if !c.Enabled {
		return Channels{}
	}
	if ch, ok := c.Events[kind]; ok {
		return ch
	}
	return c.Default
}

// Notifier sends messages. Safe for concurrent use.
type Notifier struct {
	cfg Config
	// send is the SMTP transport, injectable so tests never open a socket.
	send func(cfg EmailConfig, password, subject, body string) error
}

func New(cfg Config) *Notifier {
	return &Notifier{cfg: cfg, send: sendSMTP}
}

// Config returns the notifier's settings.
func (n *Notifier) Config() Config { return n.cfg }

// Notify delivers one message to the channels configured for its kind.
//
// Delivery is asynchronous and best-effort: this is called from the usage
// poller, and neither a stalled notification daemon nor an unreachable SMTP
// server may hold that up.
func (n *Notifier) Notify(kind, title, body, urgency string) {
	ch := n.cfg.ChannelsFor(kind)
	if ch.Desktop {
		go n.desktop(title, body, urgency)
	}
	if ch.Email {
		go n.email(title, body)
	}
}

// desktop posts through notify-send, which on this machine lands in the shell's
// notification centre. Absent elsewhere, where the log line is the record.
func (n *Notifier) desktop(title, body, urgency string) {
	if runtime.GOOS != "linux" {
		log.Printf("[notify] %s — %s", title, body)
		return
	}
	if urgency == "" {
		urgency = "normal"
	}
	cmd := exec.Command("notify-send",
		"--app-name=claumon", "--urgency="+urgency,
		"--icon=utilities-system-monitor", title, body)
	if err := cmd.Run(); err != nil {
		log.Printf("[notify] notify-send failed: %v", err)
	}
}

// email sends with three attempts and a widening delay. A bridge that is still
// starting up is the common failure and it recovers within a minute; a
// permanent failure is logged rather than retried forever.
func (n *Notifier) email(subject, body string) {
	password, err := ResolvePassword(n.cfg.Email)
	if err != nil {
		log.Printf("[notify] email password unavailable: %v", err)
		return
	}
	delay := 5 * time.Second
	for attempt := 1; attempt <= 3; attempt++ {
		err = n.send(n.cfg.Email, password, subject, body)
		if err == nil {
			return
		}
		if attempt < 3 {
			log.Printf("[notify] email attempt %d failed, retrying in %v: %v", attempt, delay, err)
			time.Sleep(delay)
			delay *= 3
		}
	}
	log.Printf("[notify] email failed after 3 attempts: %v", err)
	// Fall back to the desktop so a failed email is never a silent loss.
	n.desktop("claumon could not send email", subject, "critical")
}

// ResolvePassword runs the configured command and returns its trimmed output.
func ResolvePassword(cfg EmailConfig) (string, error) {
	if strings.TrimSpace(cfg.PasswordCommand) == "" {
		return "", fmt.Errorf("no password_command configured")
	}
	out, err := exec.Command("sh", "-c", cfg.PasswordCommand).Output()
	if err != nil {
		return "", fmt.Errorf("password_command failed: %w", err)
	}
	pw := strings.TrimSpace(string(out))
	if pw == "" {
		return "", fmt.Errorf("password_command produced no output")
	}
	return pw, nil
}

// BuildMessage renders an RFC 5322 message.
func BuildMessage(cfg EmailConfig, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", cfg.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	// Dot-stuffing: a line that is a single dot would end the DATA command.
	b.WriteString(strings.ReplaceAll(body, "\r\n.\r\n", "\r\n..\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}

// sendSMTP delivers over STARTTLS.
//
// Certificate verification is disabled deliberately: the target is a Proton
// Bridge on loopback presenting a self-signed certificate for a hostname that
// cannot be validated. The connection never leaves the machine, so the
// exposure is the loopback interface rather than the network.
func sendSMTP(cfg EmailConfig, password, subject, body string) error {
	addr := net.JoinHostPort(cfg.SMTPHost, fmt.Sprint(cfg.SMTPPort))
	client, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", addr, err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsCfg := &tls.Config{
			ServerName:         cfg.SMTPHost,
			InsecureSkipVerify: true, // loopback bridge, self-signed
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if cfg.SMTPUser != "" {
		auth := smtp.PlainAuth("", cfg.SMTPUser, password, cfg.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(cfg.To); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(BuildMessage(cfg, subject, body)); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing message: %w", err)
	}
	return client.Quit()
}
