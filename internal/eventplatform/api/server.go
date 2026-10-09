// Package api exposes tenant management contracts and the gateway's internal authentication endpoint.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/identity"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/store"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

type Server struct {
	Control       store.Control
	Notifications store.Notifications
	Projections   store.Projections
	Verify        identity.Verifier
	RetentionDays int
	Metrics       http.Handler
	Observer      monitor.Observer
}
type endpoint func(context.Context, domain.Principal, *http.Request) (any, error)

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	resource.LogError("api_response", json.NewEncoder(w).Encode(value))
}

func decode(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	defer resource.Close(r.Body)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(value); e != nil {
		return domain.ErrInvalid
	}
	if e := decoder.Decode(new(any)); !errors.Is(e, io.EOF) {
		return domain.ErrInvalid
	}
	return nil
}

func (s *Server) protected(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := monitor.ID()
		w.Header().Set("X-Request-ID", id)
		ctx, cancel := context.WithTimeout(propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header)), 10*time.Second)
		defer cancel()
		ctx, span := otel.Tracer("event-platform-api").Start(ctx, "api.request")
		defer span.End()
		started := time.Now()
		status := http.StatusUnauthorized
		if s.Observer != nil {
			var end func(error)
			ctx, end = s.Observer.Begin(ctx, "api")
			defer func() {
				var err error
				if status >= 400 {
					err = errors.New("request rejected")
				}
				end(err)
			}()
		}
		defer func() {
			sc := span.SpanContext()
			slog.InfoContext(ctx, "api_request", "request_id", id, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(), "route", r.Pattern, "method", r.Method, "status", status, "duration_ms", time.Since(started).Milliseconds())
		}()
		principal, e := s.Verify.Verify(ctx, r.Header.Get("Authorization"))
		if e != nil {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "request_id": id})
			return
		}
		value, e := fn(ctx, principal, r)
		status = http.StatusOK
		if e != nil {
			switch {
			case errors.Is(e, domain.ErrForbidden):
				status = http.StatusForbidden
			case errors.Is(e, domain.ErrNotFound):
				status = http.StatusNotFound
			case errors.Is(e, domain.ErrConflict):
				status = http.StatusConflict
			case errors.Is(e, domain.ErrQuota):
				status = http.StatusTooManyRequests
			case errors.Is(e, domain.ErrInvalid):
				status = http.StatusBadRequest
			default:
				status = http.StatusServiceUnavailable
			}
			code := http.StatusText(status)
			slog.WarnContext(ctx, "api_failed", "request_id", id, "status", status)
			value = map[string]string{"error": code, "request_id": id}
		}
		write(w, status, value)
	}
}

func (s *Server) Handler() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /internal/auth", s.protected(func(_ context.Context, p domain.Principal, _ *http.Request) (any, error) {
		return map[string]string{"subject": p.Subject}, nil
	}))
	m.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { write(w, http.StatusOK, map[string]bool{"live": true}) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		status := http.StatusOK
		if e := s.Control.DB.SQL.PingContext(ctx); e != nil {
			status = http.StatusServiceUnavailable
		}
		write(w, status, map[string]bool{"ready": status == http.StatusOK})
	})
	if s.Metrics != nil {
		m.Handle("GET /metrics", s.Metrics)
	}
	m.HandleFunc("GET /api/v1/tenants", s.protected(func(ctx context.Context, p domain.Principal, _ *http.Request) (any, error) {
		return s.Control.Tenants(ctx, p)
	}))
	m.HandleFunc("POST /api/v1/tenants", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		var t domain.Tenant
		if e := decode(nil, r, &t); e != nil {
			return nil, e
		}
		return s.Control.CreateTenant(ctx, p, t)
	}))
	m.HandleFunc("GET /api/v1/tenants/{tenant}/members", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		return s.Control.Members(ctx, p, r.PathValue("tenant"))
	}))
	m.HandleFunc("PUT /api/v1/tenants/{tenant}/members/{subject}", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		var member domain.Member
		if e := decode(nil, r, &member); e != nil {
			return nil, e
		}
		member.Subject = r.PathValue("subject")
		return map[string]bool{"updated": true}, s.Control.PutMember(ctx, p, r.PathValue("tenant"), member, false)
	}))
	m.HandleFunc("DELETE /api/v1/tenants/{tenant}/members/{subject}", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		return map[string]bool{"deleted": true}, s.Control.PutMember(ctx, p, r.PathValue("tenant"), domain.Member{Subject: r.PathValue("subject")}, true)
	}))
	m.HandleFunc("GET /api/v1/tenants/{tenant}/targets", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		targets, e := s.Control.Targets(ctx, p, r.PathValue("tenant"))
		return targetViews(targets), e
	}))
	for _, method := range []string{"POST", "PUT"} {
		m.HandleFunc(method+" /api/v1/tenants/{tenant}/targets"+itemPath(method), s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
			var target domain.Target
			if e := decode(nil, r, &target); e != nil {
				return nil, e
			}
			target.ID = r.PathValue("id")
			t, e := s.Control.SaveTarget(ctx, p, r.PathValue("tenant"), target, r.Method == http.MethodPost)
			return targetViews([]domain.Target{t})[0], e
		}))
	}
	m.HandleFunc("POST /api/v1/tenants/{tenant}/credentials/rotate", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		return map[string]bool{"rotated": true}, s.Control.RotateCredentials(ctx, p, r.PathValue("tenant"))
	}))
	m.HandleFunc("GET /api/v1/tenants/{tenant}/subscriptions", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		return s.Control.Subscriptions(ctx, p, r.PathValue("tenant"))
	}))
	for _, method := range []string{"POST", "PUT"} {
		m.HandleFunc(method+" /api/v1/tenants/{tenant}/subscriptions"+itemPath(method), s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
			var sub domain.Subscription
			if e := decode(nil, r, &sub); e != nil {
				return nil, e
			}
			sub.ID = r.PathValue("id")
			return s.Control.SaveSubscription(ctx, p, r.PathValue("tenant"), sub, r.Method == http.MethodPost)
		}))
	}
	m.HandleFunc("GET /api/v1/tenants/{tenant}/notifications", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		return s.Notifications.Jobs(ctx, p, r.PathValue("tenant"))
	}))
	m.HandleFunc("POST /api/v1/tenants/{tenant}/notifications/{id}/retry", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		return map[string]bool{"scheduled": true}, s.Notifications.Retry(ctx, p, r.PathValue("tenant"), r.PathValue("id"))
	}))
	m.HandleFunc("GET /api/v1/tenants/{tenant}/statistics", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		return s.Projections.Statistics(ctx, p, r.PathValue("tenant"))
	}))
	m.HandleFunc("GET /api/v1/tenants/{tenant}/replays", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		return s.Projections.Replays(ctx, p, r.PathValue("tenant"))
	}))
	m.HandleFunc("POST /api/v1/tenants/{tenant}/replays", s.protected(func(ctx context.Context, p domain.Principal, r *http.Request) (any, error) {
		var input struct {
			From time.Time `json:"from"`
			To   time.Time `json:"to"`
		}
		if e := decode(nil, r, &input); e != nil {
			return nil, e
		}
		return s.Projections.RequestReplay(ctx, p, r.PathValue("tenant"), input.From, input.To, s.RetentionDays)
	}))
	return m
}

func itemPath(method string) string {
	if method == http.MethodPut {
		return "/{id}"
	}
	return ""
}

func targetViews(targets []domain.Target) []map[string]any {
	out := []map[string]any{}
	for _, t := range targets {
		out = append(out, map[string]any{"id": t.ID, "tenant_id": t.TenantID, "name": t.Name, "kind": t.Kind, "version": t.Version})
	}
	return out
}
