package trpchost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
)

func TestHTTPHealthAndStatus(t *testing.T) {
	root := t.TempDir()
	h := Handler(root)
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/healthz", nil))
	if r.Code != 503 {
		t.Fatal(r.Code)
	}
	monitor.AtomicJSON(filepath.Join(root, "var/status.json"), monitor.Status{Running: true, State: "outside_window", Updated: time.Now().UnixMilli()})
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/healthz", nil))
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("POST", "/status", nil))
	if r.Code != 405 {
		t.Fatal(r.Code)
	}
	monitor.AtomicJSON(filepath.Join(root, "var/status.json"), monitor.Status{Running: true, State: "blocked", Updated: time.Now().UnixMilli()})
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/healthz", nil))
	if r.Code != 503 {
		t.Fatal(r.Code)
	}
}
func TestFrameworkConfigLoopback(t *testing.T) {
	c, e := LoadConfig("../..")
	if e != nil || c.Server.Service[0].IP != "127.0.0.1" {
		t.Fatal(c, e)
	}
}

func TestStatusCacheDoesNotCacheHealth(t *testing.T) {
	root := t.TempDir()
	cache := monitor.NewMemoryCache()
	h := HandlerWithCache(root, cache)
	monitor.AtomicJSON(filepath.Join(root, "var/status.json"), monitor.Status{Running: true, State: "healthy", Updated: time.Now().UnixMilli()})
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/status", nil))
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(r.Code)
	}
	monitor.AtomicJSON(filepath.Join(root, "var/status.json"), monitor.Status{Running: false, State: "stopped", Updated: time.Now().UnixMilli()})
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/healthz", nil))
	if r.Code != 503 {
		t.Fatal("health cached", r.Code)
	}
	monitor.AtomicJSON(filepath.Join(root, "var/status.json"), monitor.Status{Running: true, State: "standby", Updated: time.Now().UnixMilli()})
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/healthz", nil))
	if r.Code != 200 {
		t.Fatal("healthy standby rejected", r.Code)
	}
}

func TestIndependentProbesAndMetrics(t *testing.T) {
	root := t.TempDir()
	if err := monitor.AtomicJSON(filepath.Join(root, "var/status.json"), monitor.Status{Running: true, State: "healthy", Updated: time.Now().UnixMilli(), NotificationState: "blocked"}); err != nil {
		t.Fatal(err)
	}
	h := HandlerWithMetrics(root, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("metric 1\n")) }))
	for path, code := range map[string]int{"/livez": 200, "/readyz": 503, "/healthz": 503, "/metrics": 200} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != code {
			t.Fatalf("%s: %d", path, r.Code)
		}
		r = httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("POST", path, nil))
		if r.Code != 405 {
			t.Fatalf("POST %s: %d", path, r.Code)
		}
	}
}
func TestRecoveryReturnsErrorAfterPanic(t *testing.T) {
	rsp, err := recovery(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) { panic("sensitive") })
	if err == nil || rsp != nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal(rsp, err)
	}
}
