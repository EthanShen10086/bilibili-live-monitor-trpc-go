package observability

import (
	"context"
	"errors"
	"fmt"
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
	for _, span := range spans {
		if strings.Contains(span.Status.Description, "secret") || strings.Contains(fmt.Sprint(span.Attributes), "secret-webhook") {
			t.Fatal("trace exposes secret")
		}
	}
}
