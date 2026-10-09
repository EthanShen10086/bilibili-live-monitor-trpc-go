package trpchost

import (
	"context"
	"fmt"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"time"
)

// A business/provider failure stays visible without a restart loop. Only a stale
// process heartbeat or stalled scheduler terminates the host for its supervisor.
func watchProgress(ctx context.Context, root string, interval, startupGrace time.Duration) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-tick.C:
			if now.Sub(started) < startupGrace {
				continue
			}
			status, err := monitor.ReadStatus(root)
			if err != nil || !status.Live(now) {
				return fmt.Errorf("worker progress watchdog expired")
			}
		}
	}
}
