// Package channels adapts durable delivery tasks to external notification providers.
package channels

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

type Sender struct {
	HTTP              *monitor.HTTP
	AllowedSMTPHosts  []string
	TestSMTPPlaintext bool
}

func (s Sender) Send(ctx context.Context, target domain.Target, text, key string) error {
	c := target.Credentials
	if target.Kind == "smtp" {
		allowed := false
		for _, host := range s.AllowedSMTPHosts {
			if c.SMTPHost == host {
				allowed = true
			}
		}
		if !allowed {
			return &monitor.RemoteError{Service: "SMTP", Code: "host_not_allowed", Retry: false}
		}
		return s.mail(ctx, c, text, key)
	}
	var config monitor.Config
	config.Notification.Mode = target.Kind
	config.Notification.Group.Webhook = "webhook"
	config.Notification.Group.Secret = "secret"
	config.Notification.Private.AppID = "app_id"
	config.Notification.Private.Secret = "secret"
	config.Notification.Private.ID = "recipient"
	config.Notification.Private.IDType = c.IDType
	values := map[string]string{"webhook": c.Webhook, "secret": c.Secret, "app_id": c.AppID, "recipient": c.Recipient}
	f := &monitor.Feishu{Config: config, HTTP: s.HTTP, Lookup: func(name string) string { return values[name] }}
	return f.Send(ctx, text, key)
}

func (s Sender) mail(ctx context.Context, c domain.Credentials, text, key string) error {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, e := dialer.DialContext(ctx, "tcp", net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort)))
	if e != nil {
		return &monitor.RemoteError{Service: "SMTP", Code: "connect", Retry: true}
	}
	defer resource.Close(conn)
	deadline := time.Now().Add(25 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if e = conn.SetDeadline(deadline); e != nil {
		return e
	}
	client, e := smtp.NewClient(conn, c.SMTPHost)
	if e != nil {
		return &monitor.RemoteError{Service: "SMTP", Code: "handshake", Retry: true}
	}
	defer resource.Close(client)
	if !s.TestSMTPPlaintext {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return &monitor.RemoteError{Service: "SMTP", Code: "tls_required", Retry: false}
		}
		if e = client.StartTLS(&tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12}); e != nil {
			return &monitor.RemoteError{Service: "SMTP", Code: "tls", Retry: true}
		}
	}
	if c.Username != "" {
		if e = client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.SMTPHost)); e != nil {
			return &monitor.RemoteError{Service: "SMTP", Code: "auth", Retry: false}
		}
	}
	if e = client.Mail(c.From); e != nil {
		return &monitor.RemoteError{Service: "SMTP", Code: "sender", Retry: false}
	}
	if e = client.Rcpt(c.Recipient); e != nil {
		return &monitor.RemoteError{Service: "SMTP", Code: "recipient", Retry: false}
	}
	writer, e := client.Data()
	if e != nil {
		return &monitor.RemoteError{Service: "SMTP", Code: "data", Retry: true}
	}
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Bilibili live notification\r\nMessage-ID: <%s@live-monitor>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", c.From, c.Recipient, key, strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))
	_, e = writer.Write([]byte(message))
	closeErr := writer.Close()
	if e != nil || closeErr != nil {
		return &monitor.RemoteError{Service: "SMTP", Code: "acceptance_unknown", Retry: true}
	}
	// DATA acknowledgement is acceptance, not inbox delivery. QUIT failure cannot undo it.
	resource.LogError("smtp_quit", client.Quit())
	return nil
}
