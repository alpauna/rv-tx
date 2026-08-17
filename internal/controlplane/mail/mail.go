// Package mail sends invite/reset emails via plain SMTP+STARTTLS.
// Config env vars deliberately match the dnsmasq-ui project's own
// smtp.env naming (SMTP_SERVER/PORT/USER/PASSWORD/FROM) so the same
// relay/credentials can be reused verbatim across both projects.
package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"
)

const dialTimeout = 10 * time.Second

type Config struct {
	Server   string
	Port     string
	User     string
	Password string
	From     string
}

// Configured reports whether enough config is present to attempt
// sending -- callers use this to fail a request early with a clear
// error instead of hitting a confusing dial failure.
func (c Config) Configured() bool {
	return c.Server != ""
}

// Send delivers a single plain-text email via STARTTLS, authenticating
// only if a user is configured (some relays are IP-allowlisted and
// need no auth at all).
func Send(cfg Config, to, subject, body string) error {
	if !cfg.Configured() {
		return fmt.Errorf("mail not configured (SMTP_SERVER unset)")
	}

	addr := net.JoinHostPort(cfg.Server, cfg.Port)
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, cfg.Server)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if err := client.StartTLS(&tls.Config{ServerName: cfg.Server}); err != nil {
		return fmt.Errorf("starttls: %w", err)
	}

	if cfg.User != "" {
		auth := smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Server)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	from := cfg.From
	if from == "" {
		from = cfg.User
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n", from, to, subject, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close message: %w", err)
	}

	return client.Quit()
}
