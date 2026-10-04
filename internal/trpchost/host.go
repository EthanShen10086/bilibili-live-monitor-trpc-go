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
)

const ServiceName = "trpc.live.monitor.Status"

func Handler(root string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		s, e := monitor.ReadStatus(root)
		if e != nil {
			http.Error(w, "worker not ready", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		s, e := monitor.ReadStatus(root)
		if e != nil || !s.Running || time.Now().UnixMilli()-s.Updated > 20000 || (s.State != "healthy" && s.State != "outside_window") {
			http.Error(w, "worker unhealthy", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
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
	if len(c.Plugins) > 0 {
		return nil, fmt.Errorf("this standalone adapter does not initialize external plugins")
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
	server := trpc.NewServerWithConfig(cfg)
	thttp.RegisterNoProtocolServiceMux(server.Service(ServiceName), Handler(o.Root))
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerDone := make(chan error, 1)
	serverDone := make(chan error, 1)
	server.RegisterOnShutdown(cancel)
	go func() { workerDone <- monitor.Run(workerCtx, o.Root, c, monitor.NewHTTP()) }()
	go func() { serverDone <- server.Serve() }()
	var runErr error
	workerFinished := false
	select {
	case runErr = <-workerDone:
		workerFinished = true
	case runErr = <-serverDone:
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
		case <-time.After(30 * time.Second):
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
