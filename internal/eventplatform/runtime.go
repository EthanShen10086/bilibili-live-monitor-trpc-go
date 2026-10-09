package eventplatform

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"trpc.group/trpc-go/trpc-go"
	thttp "trpc.group/trpc-go/trpc-go/http"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/api"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/bus"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/channels"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/identity"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/store"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/observability"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

func Run(ctx context.Context, c Config, role, configPath string) error {
	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, e := store.Open(initCtx, c.DSN, c.Vault)
	if e != nil {
		return e
	}
	defer resource.Close(db)
	if role == "migrate" {
		return db.Migrate(initCtx)
	}
	if e = db.CheckSchema(initCtx); e != nil {
		return e
	}
	var legacy monitor.Config
	legacy.Observability.Tracing = c.Tracing
	legacy.Observability.ServiceName = "live-platform-" + role
	telemetry, e := observability.New(ctx, legacy)
	if e != nil {
		return e
	}
	defer resource.Close(telemetry)
	h := monitor.NewHTTP()
	h.SetObserver(telemetry)
	defer h.Client.CloseIdleConnections()
	control := store.Control{DB: db}
	events := store.Events{DB: db}
	notifications := store.Notifications{DB: db}
	projections := store.Projections{DB: db}
	owner := monitor.ID()
	if role == "api" {
		verify, e := identity.New(initCtx, c.Issuer, c.Audience, c.AdminSubjects)
		if e != nil {
			return e
		}
		handler := &api.Server{Control: control, Notifications: notifications, Projections: projections, Verify: verify, RetentionDays: c.RetentionDays, Metrics: telemetry.Handler()}
		return serve(ctx, configPath, handler)
	}
	if role == "detector" {
		return tick(ctx, time.Second, func(ctx context.Context) error {
			rooms, e := events.Rooms(ctx)
			if e != nil {
				return e
			}
			for _, room := range rooms {
				if e = detect(ctx, room, owner, h, control, events, notifications); e != nil {
					return e
				}
			}
			return nil
		})
	}
	if role == "sender" {
		sender := channels.Sender{HTTP: h, AllowedSMTPHosts: c.SMTPHosts}
		return tick(ctx, time.Second, func(ctx context.Context) error {
			job, e := notifications.Claim(ctx, owner)
			if e != nil || job == nil {
				return e
			}
			target, e := control.ResolveTarget(ctx, job.TenantID, job.TargetID)
			if e == nil {
				deliveryCtx, end := context.WithTimeout(ctx, 30*time.Second)
				e = sender.Send(deliveryCtx, target, job.Text, job.ID)
				end()
			}
			finishCtx, end := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer end()
			return notifications.Finish(finishCtx, *job, e)
		})
	}
	if role != "relay" && role != "router" && role != "analytics" {
		return errors.New("role must be migrate|api|detector|relay|router|sender|analytics")
	}
	kafkaConfig := c.Kafka
	if role == "router" {
		kafkaConfig.Group = "live-notifications-v1"
	}
	if role == "analytics" {
		kafkaConfig.Group = "live-analytics-v1"
	}
	client, e := bus.Open(initCtx, kafkaConfig)
	if e != nil {
		return e
	}
	defer resource.Close(client)
	if role == "relay" {
		return tick(ctx, time.Second, func(ctx context.Context) error { return events.PublishBatch(ctx, client.Publish) })
	}
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	background := make(chan error, 1)
	go func() {
		background <- tick(workerCtx, 5*time.Second, func(ctx context.Context) error {
			if role == "analytics" {
				return projections.RebuildNext(ctx)
			}
			snapshots, e := events.LiveSnapshots(ctx)
			if e != nil {
				return e
			}
			for _, event := range snapshots {
				if time.Since(event.Time) > 2*time.Hour {
					continue
				}
				subs, e := control.RoomSubscriptions(ctx, event.Data.RequestedRoom)
				if e != nil {
					return e
				}
				if e = notifications.Route(ctx, event, subs, true); e != nil {
					return e
				}
			}
			return nil
		})
	}()
	e = client.Consume(workerCtx, func(ctx context.Context, event domain.Event) error {
		ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": event.TraceParent, "tracestate": event.TraceState})
		ctx, span := otel.Tracer("event-platform").Start(ctx, role+".consume")
		defer span.End()
		if role == "analytics" {
			return events.Project(ctx, event)
		}
		subs, e := control.RoomSubscriptions(ctx, event.Data.RequestedRoom)
		if e != nil {
			return e
		}
		return notifications.Route(ctx, event, subs, false)
	}, func(ctx context.Context, id string) error {
		return events.Deadletter(ctx, role, id, "invalid_event_contract")
	})
	stop()
	<-background
	return e
}

func tick(ctx context.Context, interval time.Duration, fn func(context.Context) error) error {
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		workCtx, end := context.WithTimeout(ctx, 15*time.Second)
		e := fn(workCtx)
		end()
		if e != nil {
			slog.Warn("platform_operation_failed", "error_type", typeName(e))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
}

func typeName(e error) string {
	if errors.Is(e, domain.ErrConflict) {
		return "conflict"
	}
	return "dependency_or_operation"
}

func detect(ctx context.Context, room int64, owner string, h monitor.Detector, control store.Control, events store.Events, notifications store.Notifications) error {
	subs, e := control.RoomSubscriptions(ctx, room)
	if e != nil {
		return e
	}
	active := []domain.Subscription{}
	delay := time.Hour
	for _, s := range subs {
		policy := s.Policy.MonitorConfig(room)
		if policy.InWindow(time.Now()) {
			active = append(active, s)
			delay = min(delay, time.Duration(s.Policy.IntervalSeconds)*time.Second)
		}
	}
	if len(active) == 0 {
		return nil
	}
	acquired, e := events.Acquire(ctx, room, owner)
	if e != nil || !acquired {
		return e
	}
	ctx, span := otel.Tracer("event-platform").Start(ctx, "detector.probe")
	defer span.End()
	config := active[0].Policy.MonitorConfig(room)
	o, e := h.Probe(ctx, config)
	if e != nil {
		if delayed := events.Delay(ctx, room, owner, monitor.RetryDelay(e, 1, int(delay.Seconds()))); delayed != nil {
			return delayed
		}
		return e
	}
	if o.Live {
		snapshots, e := events.LiveSnapshots(ctx)
		if e != nil {
			return e
		}
		for _, snapshot := range snapshots {
			if snapshot.Data.RequestedRoom != room {
				continue
			}
			if snapshot.Data.Observation.Start != o.Start {
				continue
			}
			all := true
			slow := time.Hour
			for _, s := range active {
				sent, e := notifications.AllSent(ctx, s, snapshot.ID)
				if e != nil {
					return e
				}
				all = all && sent
				slow = min(slow, time.Duration(s.Policy.NotifiedSeconds)*time.Second)
			}
			if all {
				delay = slow
			}
		}
	}
	return events.Observe(ctx, room, owner, o, false, delay)
}

func serve(ctx context.Context, path string, handler *api.Server) error {
	cfg, e := trpc.LoadConfig(path)
	if e != nil {
		return e
	}
	closer, e := trpc.SetupPlugins(cfg.Plugins)
	if e != nil {
		return e
	}
	defer func() { resource.LogError("platform_plugins", closer()) }()
	s := trpc.NewServerWithConfig(cfg)
	thttp.RegisterNoProtocolServiceMux(s.Service("trpc.live.platform.API"), handler.Handler())
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	select {
	case e = <-done:
		return e
	case <-ctx.Done():
		resource.LogError("platform_server_close", s.Close(nil))
		return <-done
	}
}
