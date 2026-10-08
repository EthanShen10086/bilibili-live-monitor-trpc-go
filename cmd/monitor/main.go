package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/trpchost"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	o, e := monitor.ParseOptions(os.Args[1:])
	var closeLogs func()
	if e == nil && o.Managed != "" {
		closeLogs, e = monitor.CaptureManagedLogs(o.Root)
	}
	if e == nil {
		if len(o.Args) > 0 && o.Args[0] == "run" {
			e = trpchost.Run(ctx, o)
		} else {
			e = monitor.CLI(ctx, o)
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		if closeLogs != nil {
			closeLogs()
		}
		os.Exit(1)
	}
	if closeLogs != nil {
		closeLogs()
	}
}
