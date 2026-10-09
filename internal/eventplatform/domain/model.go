// Package domain defines versioned event-platform contracts, independent of transports.
package domain

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
)

var (
	ErrForbidden = errors.New("forbidden")
	ErrConflict  = errors.New("version conflict or duplicate resource")
	ErrNotFound  = errors.New("resource not found")
	ErrQuota     = errors.New("tenant quota exceeded")
	ErrInvalid   = errors.New("invalid input")
)

type Principal struct {
	Subject       string
	PlatformAdmin bool
}
type Tenant struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	MaxSubscriptions int    `json:"max_subscriptions"`
	MaxTargets       int    `json:"max_targets"`
}
type Member struct {
	Subject string `json:"subject"`
	Role    string `json:"role"`
}

func Can(role, action string) bool {
	switch role {
	case "owner":
		return true
	case "admin":
		return action != "ownership"
	case "operator":
		return action == "read" || action == "operate"
	case "viewer":
		return action == "read"
	}
	return false
}

func ValidRole(role string) bool {
	return role == "owner" || role == "admin" || role == "operator" || role == "viewer"
}

type Policy struct {
	Timezone        string `json:"timezone"`
	Weekdays        []int  `json:"weekdays"`
	Start           string `json:"start"`
	End             string `json:"end"`
	IntervalSeconds int    `json:"interval_seconds"`
	NotifiedSeconds int    `json:"notified_seconds"`
	TTLMinutes      int    `json:"ttl_minutes"`
}

func DefaultPolicy() Policy {
	return Policy{"Asia/Shanghai", []int{3, 5, 6, 7}, "18:00", "24:00", 60, 300, 30}
}

func (p Policy) MonitorConfig(room int64) monitor.Config {
	var c monitor.Config
	c.Subscription.RoomID = room
	c.Detector.Mode = "polling"
	c.Detector.Polling.Interval = &p.IntervalSeconds
	c.Detector.Polling.Timeout = 5
	c.Schedule.Timezone = p.Timezone
	c.Schedule.Weekdays = p.Weekdays
	c.Schedule.Start = p.Start
	c.Schedule.End = p.End
	c.Notification.TTL = p.TTLMinutes
	return c
}

func (p Policy) Validate() error {
	if p.IntervalSeconds < 10 || p.IntervalSeconds > 3600 || p.NotifiedSeconds < p.IntervalSeconds || p.NotifiedSeconds > 3600 || p.TTLMinutes < 1 || p.TTLMinutes > 1440 || len(p.Weekdays) == 0 {
		return ErrInvalid
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return ErrInvalid
	}
	for _, d := range p.Weekdays {
		if d < 1 || d > 7 {
			return ErrInvalid
		}
	}
	for _, v := range []string{p.Start, p.End} {
		if v == "24:00" {
			continue
		}
		if _, err := time.Parse("15:04", v); err != nil {
			return ErrInvalid
		}
	}
	if p.Start == "24:00" || p.Start >= p.End {
		return ErrInvalid
	}
	return nil
}

type Subscription struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	RoomID    int64     `json:"room_id"`
	Enabled   bool      `json:"enabled"`
	Policy    Policy    `json:"policy"`
	TargetIDs []string  `json:"target_ids"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

func (s Subscription) Validate() error {
	if s.RoomID <= 0 || len(s.TargetIDs) == 0 || len(s.TargetIDs) > 20 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range s.TargetIDs {
		if id == "" || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return s.Policy.Validate()
}

type Target struct {
	ID          string      `json:"id"`
	TenantID    string      `json:"tenant_id"`
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	Version     int64       `json:"version"`
	Credentials Credentials `json:"credentials,omitempty"`
}

// Credentials are accepted on writes only, encrypted at rest and never returned by APIs.
type Credentials struct {
	Webhook   string `json:"webhook,omitempty"`
	Secret    string `json:"secret,omitempty"`
	AppID     string `json:"app_id,omitempty"`
	Recipient string `json:"recipient,omitempty"`
	IDType    string `json:"id_type,omitempty"`
	SMTPHost  string `json:"smtp_host,omitempty"`
	SMTPPort  int    `json:"smtp_port,omitempty"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	From      string `json:"from,omitempty"`
}

func (t Target) Validate() error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 100 {
		return ErrInvalid
	}
	c := t.Credentials
	switch t.Kind {
	case "feishu_group":
		u, e := url.Parse(c.Webhook)
		if e != nil || u.Scheme != "https" || u.Host != "open.feishu.cn" || !strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") || u.User != nil || u.RawQuery != "" || c.Secret == "" {
			return ErrInvalid
		}
	case "feishu_private":
		if c.AppID == "" || c.Secret == "" || c.Recipient == "" || (c.IDType != "open_id" && c.IDType != "user_id" && c.IDType != "union_id" && c.IDType != "email") {
			return ErrInvalid
		}
	case "smtp":
		if c.SMTPHost == "" || strings.ContainsAny(c.SMTPHost, "\r\n/:") || c.SMTPPort < 1 || c.SMTPPort > 65535 {
			return ErrInvalid
		}
		for _, v := range []string{c.From, c.Recipient} {
			a, e := mail.ParseAddress(v)
			if e != nil || a.Address != v || strings.ContainsAny(v, "\r\n") {
				return ErrInvalid
			}
		}
	default:
		return ErrInvalid
	}
	return nil
}

type Event struct {
	SpecVersion     string    `json:"specversion"`
	ID              string    `json:"id"`
	Source          string    `json:"source"`
	Type            string    `json:"type"`
	Subject         string    `json:"subject"`
	Time            time.Time `json:"time"`
	DataContentType string    `json:"datacontenttype"`
	Data            EventData `json:"data"`
	TraceParent     string    `json:"traceparent,omitempty"`
	TraceState      string    `json:"tracestate,omitempty"`
}
type EventData struct {
	SchemaVersion int                 `json:"schema_version"`
	RequestedRoom int64               `json:"requested_room"`
	SessionKey    string              `json:"session_key"`
	Catchup       bool                `json:"catchup"`
	Observation   monitor.Observation `json:"observation"`
}

func (e Event) Validate() error {
	if e.SpecVersion != "1.0" || e.ID == "" || e.Source != "/bilibili/rooms" || e.Time.IsZero() || e.Data.SchemaVersion != 1 || e.Data.RequestedRoom <= 0 || e.Data.SessionKey == "" || (e.Type != "live.started.v1" && e.Type != "live.ended.v1") {
		return fmt.Errorf("event contract: %w", ErrInvalid)
	}
	return nil
}

type Job struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	SubscriptionID string    `json:"subscription_id"`
	TargetID       string    `json:"target_id"`
	EventID        string    `json:"event_id"`
	State          string    `json:"state"`
	Attempts       int       `json:"attempts"`
	Expires        time.Time `json:"expires"`
	Next           time.Time `json:"next"`
	LeaseOwner     string    `json:"-"`
	Text           string    `json:"-"`
	LatencyMillis  int64     `json:"latency_ms"`
	Target         Target    `json:"-"`
}
