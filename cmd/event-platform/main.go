package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	role := flag.String("role", "api", "migrate|api|detector|relay|router|sender|analytics")
	config := flag.String("trpc-config", "deploy/event-platform/trpc_go.yaml", "tRPC configuration")
	flag.Parse()
	slog.SetDefault(slog.Default().With("service", "live-platform", "role", *role))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	c, e := eventplatform.LoadConfig()
	if e == nil {
		e = eventplatform.Run(ctx, c, *role, *config)
	}
	if e != nil {
		slog.Error("platform_failed", "reason", "configuration_or_dependency_failure")
		os.Exit(1)
	}
}
