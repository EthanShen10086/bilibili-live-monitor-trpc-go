package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	role := flag.String("role", "api", "migrate|api|detector|relay|router|sender|analytics")
	config := flag.String("trpc-config", "deploy/event-platform/trpc_go.yaml", "tRPC configuration")
	legacyRoot := flag.String("legacy-root", "", "Legacy configuration directory")
	legacyDB := flag.String("legacy-db", "", "Stopped legacy SQLite backup")
	owner := flag.String("owner-subject", "", "Keycloak owner subject for import")
	stopped := flag.Bool("confirm-old-sender-stopped", false, "Explicit declaration that legacy sending is stopped")
	flag.Parse()
	slog.SetDefault(slog.Default().With("service", "live-platform", "role", *role))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	c, e := eventplatform.LoadConfig()
	if e == nil {
		if *role == "import" {
			report, err := eventplatform.Import(ctx, c, *legacyRoot, *legacyDB, *owner, *stopped)
			e = err
			if e == nil {
				resource.LogError("import_report", json.NewEncoder(os.Stdout).Encode(report))
			}
		} else {
			e = eventplatform.Run(ctx, c, *role, *config)
		}
	}
	if e != nil {
		slog.Error("platform_failed", "reason", "configuration_or_dependency_failure")
		os.Exit(1)
	}
}
