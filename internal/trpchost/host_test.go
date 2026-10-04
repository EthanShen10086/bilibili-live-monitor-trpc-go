package trpchost

import (
	"net/http/httptest"
	"path/filepath"
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
