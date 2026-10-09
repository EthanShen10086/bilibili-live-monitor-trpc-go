package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/trpchost"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	o, e := monitor.ParseOptions(os.Args[1:])
	var closeLogs func()
	// Containers and systemd collect standard streams. Descriptor capture is local only.
	if e == nil && o.Managed == "local" {
		closeLogs, e = monitor.CaptureManagedLogs(o.Root)
	}
	if e == nil {
		if len(o.Args) > 0 && o.Args[0] == "run" {
			slog.Info("worker_starting", "managed", o.Managed)
			e = trpchost.Run(ctx, o)
		} else {
			e = monitor.CLI(ctx, o)
		}
	}
	if e != nil {
		slog.Error("command_failed", "error", e.Error())
		if closeLogs != nil {
			closeLogs()
		}
		os.Exit(1)
	}
	if closeLogs != nil {
		closeLogs()
	}
}
