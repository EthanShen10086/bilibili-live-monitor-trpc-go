// Package observability provides bounded-cardinality metrics and optional OTLP traces.
package observability

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type Telemetry struct {
	Registry                                            *prometheus.Registry
	operations                                          *prometheus.CounterVec
	duration                                            *prometheus.HistogramVec
	queue                                               *prometheus.GaugeVec
	state                                               *prometheus.GaugeVec
	lastProgress, lastObservation, lastSent, leadership prometheus.Gauge
	mu                                                  sync.Mutex
	tracer                                              trace.Tracer
	provider                                            *sdktrace.TracerProvider
}

func New(ctx context.Context, c monitor.Config) (*Telemetry, error) {
	t := &Telemetry{Registry: prometheus.NewRegistry(), tracer: noop.NewTracerProvider().Tracer("live-monitor")}
	t.operations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "live_monitor_operations_total", Help: "Completed operations by fixed operation and result."}, []string{"operation", "result"})
	t.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "live_monitor_operation_duration_seconds", Help: "Operation duration including retries.", Buckets: []float64{.05, .1, .5, 1, 2, 5, 10, 20, 40}}, []string{"operation"})
	t.queue = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "live_monitor_jobs", Help: "Durable subscription jobs by state."}, []string{"state"})
	t.state = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "live_monitor_health", Help: "Current process, readiness and business health."}, []string{"kind"})
	t.lastProgress = prometheus.NewGauge(prometheus.GaugeOpts{Name: "live_monitor_last_progress_timestamp_seconds", Help: "Last scheduling progress timestamp."})
	t.lastObservation = prometheus.NewGauge(prometheus.GaugeOpts{Name: "live_monitor_last_observation_timestamp_seconds", Help: "Last successful observation timestamp."})
	t.lastSent = prometheus.NewGauge(prometheus.GaugeOpts{Name: "live_monitor_last_sent_timestamp_seconds", Help: "Last notification accepted and committed."})
	t.leadership = prometheus.NewGauge(prometheus.GaugeOpts{Name: "live_monitor_leadership", Help: "Whether this process owns the detector lease."})
	t.Registry.MustRegister(t.operations, t.duration, t.queue, t.state, t.lastProgress, t.lastObservation, t.lastSent, t.leadership, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	for _, op := range []string{"detector", "notification", "http"} {
		for _, result := range []string{"success", "error"} {
			t.operations.WithLabelValues(op, result)
		}
	}
	revision := "unknown"
	if b, ok := debug.ReadBuildInfo(); ok {
		for _, s := range b.Settings {
			if s.Key == "vcs.revision" {
				revision = s.Value
			}
		}
	}
	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "live_monitor_build_info", Help: "Build revision."}, []string{"revision"})
	info.WithLabelValues(revision).Set(1)
	t.Registry.MustRegister(info)
	if c.Observability.Tracing {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("initialize OTLP traces: %w", err)
		}
		ratio := .1
		if c.Observability.SampleRatio != nil {
			ratio = *c.Observability.SampleRatio
		}
		name := c.Observability.ServiceName
		if name == "" {
			name = "bilibili-live-monitor"
		}
		t.provider = sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(256), sdktrace.WithMaxExportBatchSize(64), sdktrace.WithExportTimeout(3*time.Second)), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))), sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", name))))
		t.tracer = t.provider.Tracer("live-monitor")
	}
	return t, nil
}
func (t *Telemetry) Begin(ctx context.Context, op string) (context.Context, func(error)) {
	ctx, span := t.tracer.Start(ctx, op)
	start := time.Now()
	return ctx, func(err error) {
		result := "success"
		if err != nil {
			result = "error"
			span.SetStatus(codes.Error, "operation failed")
			span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
		}
		t.operations.WithLabelValues(op, result).Inc()
		t.duration.WithLabelValues(op).Observe(time.Since(start).Seconds())
		span.End()
	}
}
func (t *Telemetry) Report(s monitor.Status) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, v := range map[string]bool{"live": s.Live(now), "ready": s.Ready(now), "business": s.BusinessHealthy(now)} {
		value := 0.
		if v {
			value = 1
		}
		t.state.WithLabelValues(k).Set(value)
	}
	counts, _ := s.Pending.(map[string]int)
	for _, state := range []string{"pending", "sent", "failed", "expired"} {
		t.queue.WithLabelValues(state).Set(float64(counts[state]))
	}
	t.lastProgress.Set(float64(s.Progress) / 1000)
	t.lastObservation.Set(float64(s.LastObservation) / 1000)
	t.lastSent.Set(float64(s.LastSent) / 1000)
	leader := 0.
	if s.Leadership {
		leader = 1
	}
	t.leadership.Set(leader)
}
func (t *Telemetry) Handler() http.Handler {
	return promhttp.HandlerFor(t.Registry, promhttp.HandlerOpts{Timeout: 2 * time.Second})
}
func (t *Telemetry) Close() error {
	if t.provider == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.provider.Shutdown(ctx)
}
