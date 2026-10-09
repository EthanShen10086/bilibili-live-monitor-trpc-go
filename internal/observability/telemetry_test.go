package observability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestMetricsAndSpansExcludeProviderSecrets(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	telemetry, err := New(context.Background(), monitor.Config{})
	if err != nil {
		t.Fatal(err)
	}
	exporter := tracetest.NewInMemoryExporter()
	telemetry.provider = sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	telemetry.tracer = telemetry.provider.Tracer("test")
	defer telemetry.Close()
	ctx, finish := telemetry.Begin(context.Background(), "notification")
	_, httpFinish := telemetry.Begin(ctx, "http")
	httpFinish(nil)
	finish(errors.New("secret-webhook-token"))
	now := time.Now().UnixMilli()
	telemetry.Report(monitor.Status{Running: true, State: "healthy", Updated: now, Progress: now, NotificationState: "blocked", Pending: map[string]int{"failed": 1}})
	response := httptest.NewRecorder()
	telemetry.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{`live_monitor_operations_total{operation="notification",result="error"} 1`, `live_monitor_jobs{state="failed"} 1`, `live_monitor_health{kind="live"} 1`, `live_monitor_health{kind="business"} 0`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	if strings.Contains(body, "secret-webhook-token") {
		t.Fatal("metrics expose secret")
	}
	spans := exporter.GetSpans()
	if len(spans) != 2 || spans[0].Parent.TraceID() != spans[1].SpanContext.TraceID() || spans[1].Status.Code != codes.Error {
		t.Fatalf("trace hierarchy/error status: %+v", spans)
	}
	if strings.Contains(logs.String(), "secret-webhook-token") || !strings.Contains(logs.String(), spans[1].SpanContext.TraceID().String()) {
		t.Fatal("secret in logs or missing trace correlation")
	}
	for _, span := range spans {
		if strings.Contains(span.Status.Description, "secret") || strings.Contains(fmt.Sprint(span.Attributes), "secret-webhook") {
			t.Fatal("trace exposes secret")
		}
	}
}

func TestConfiguredOTLPExporterFlushesOnShutdown(t *testing.T) {
	received := make(chan []byte, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	c := monitor.Config{}
	c.Observability.Tracing = true
	ratio := 1.
	c.Observability.SampleRatio = &ratio
	telemetry, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	_, finish := telemetry.Begin(context.Background(), "detector")
	finish(errors.New("hidden-provider-token"))
	if err = telemetry.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-received:
		if !bytes.Contains(payload, []byte("detector")) || bytes.Contains(payload, []byte("hidden-provider-token")) {
			t.Fatal("invalid or sensitive exported trace")
		}
	case <-time.After(time.Second):
		t.Fatal("traces not exported on shutdown")
	}
}
