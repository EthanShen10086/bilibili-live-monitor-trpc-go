package trpchost

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
	trpc "trpc.group/trpc-go/trpc-go"
	thttp "trpc.group/trpc-go/trpc-go/http"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/observability"
)

const ServiceName = "trpc.live.monitor.Status"

func Handler(root string) http.Handler { return HandlerWithCache(root, nil) }
func HandlerWithCache(root string, cache monitor.Cache) http.Handler {
	return HandlerWithMetrics(root, cache, nil)
}

func HandlerWithMetrics(root string, cache monitor.Cache, metrics http.Handler) http.Handler {
	cacheKey := "status:" + monitor.ID()
	mux := http.NewServeMux()
	if metrics != nil {
		mux.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.WriteHeader(405)
				return
			}
			metrics.ServeHTTP(w, r)
		}))
	}
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		if cache != nil {
			if b, ok := cache.Get(r.Context(), cacheKey); ok {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.Write(b)
				return
			}
		}
		s, e := monitor.ReadStatus(root)
		if e != nil {
			http.Error(w, "worker not ready", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(s)
		if cache != nil {
			cache.Put(r.Context(), cacheKey, b, 2*time.Second)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})
	probe := func(check func(monitor.Status, time.Time) bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				w.WriteHeader(405)
				return
			}
			s, e := monitor.ReadStatus(root)
			if e != nil || !check(s, time.Now()) {
				http.Error(w, "worker unhealthy", 503)
				return
			}
			w.Write([]byte("ok\n"))
		}
	}
	mux.HandleFunc("/livez", probe(monitor.Status.Live))
	mux.HandleFunc("/readyz", probe(monitor.Status.Ready))
	mux.HandleFunc("/healthz", probe(monitor.Status.BusinessHealthy))
	return mux
}

func LoadConfig(root string) (*trpc.Config, error) {
	b, e := os.ReadFile(filepath.Join(root, "trpc_go.yaml"))
	if e != nil {
		return nil, e
	}
	var c trpc.Config
	if yaml.Unmarshal(b, &c) != nil {
		return nil, fmt.Errorf("invalid trpc_go.yaml")
	}
	if len(c.Server.Service) != 1 || c.Server.Service[0].Name != ServiceName || c.Server.Service[0].Protocol != "http" {
		return nil, fmt.Errorf("expected one HTTP Status service")
	}
	service := c.Server.Service[0]
	if service.Address != "" || service.Nic != "" || c.Server.Admin.Nic != "" {
		return nil, fmt.Errorf("use explicit loopback IPs, not address/NIC overrides")
	}
	for _, ip := range []string{service.IP, c.Server.Admin.IP} {
		v := net.ParseIP(ip)
		if v == nil || !v.IsLoopback() {
			return nil, fmt.Errorf("tRPC status/admin must listen on loopback")
		}
	}
	if service.Port == 0 || c.Server.Admin.Port == 0 || service.Port == c.Server.Admin.Port {
		return nil, fmt.Errorf("invalid status/admin ports")
	}
	return &c, nil
}

func Run(ctx context.Context, o monitor.Options) error {
	c, e := monitor.PrepareRun(ctx, o)
	if e != nil {
		return e
	}
	cfg, e := LoadConfig(o.Root)
	if e != nil {
		return e
	}
	closePlugins, e := trpc.SetupPlugins(cfg.Plugins)
	if e != nil {
		return fmt.Errorf("initialize tRPC plugins: %w", e)
	}
	defer closePlugins()
	if e = trpc.SetupClients(&cfg.Client); e != nil {
		return fmt.Errorf("initialize tRPC clients: %w", e)
	}
	server := trpc.NewServerWithConfig(cfg)
	cache, e := monitor.OpenCache(c)
	if e != nil {
		return e
	}
	defer cache.Close()
	telemetry, e := observability.New(ctx, c)
	if e != nil {
		return e
	}
	defer telemetry.Close()
	thttp.RegisterNoProtocolServiceMux(server.Service(ServiceName), HandlerWithMetrics(o.Root, cache, telemetry.Handler()))
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerDone := make(chan error, 1)
	serverDone := make(chan error, 1)
	watchdogDone := make(chan error, 1)
	go func() { watchdogDone <- watchProgress(workerCtx, o.Root, 10*time.Second, 30*time.Second) }()
	server.RegisterOnShutdown(cancel)
	h := monitor.NewHTTP()
	h.SetObserver(telemetry)
	defer h.Client.CloseIdleConnections()
	go func() {
		workerDone <- monitor.RunWithDependencies(workerCtx, o.Root, c, h, monitor.Dependencies{Observer: telemetry})
	}()
	go func() { serverDone <- server.Serve() }()
	var runErr error
	workerFinished := false
	select {
	case runErr = <-workerDone:
		workerFinished = true
	case runErr = <-serverDone:
	case runErr = <-watchdogDone:
	case <-ctx.Done():
	}
	cancel()
	closeDone := make(chan struct{})
	go func() { server.Close(nil); close(closeDone) }()
	if !workerFinished {
		select {
		case e := <-workerDone:
			if runErr == nil {
				runErr = e
			}
		case <-time.After(65 * time.Second):
			return fmt.Errorf("worker graceful shutdown timed out")
		}
	}
	select {
	case <-closeDone:
	case <-time.After(15 * time.Second):
		return fmt.Errorf("tRPC shutdown timed out")
	}
	return runErr
}
