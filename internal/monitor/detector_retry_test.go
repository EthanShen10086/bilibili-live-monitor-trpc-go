package monitor

import (
	"context"
	"testing"
	"time"
)

type reportObserver func(Status)

func (o reportObserver) Begin(ctx context.Context, _ string) (context.Context, func(error)) {
	return ctx, func(error) {}
}
func (o reportObserver) Report(s Status) { o(s) }

func TestWorkerHonorsDetectorRetryAfter(t *testing.T) {
	c := activeConfig(t)
	one := 1
	c.Detector.Polling.IntervalMinutes = nil
	c.Detector.Polling.Interval = &one
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var retry Status
	before := time.Now()
	h := NewHTTP()
	defer h.Client.CloseIdleConnections()
	err := RunWithDependencies(ctx, t.TempDir(), c, h, Dependencies{
		Detector: testDetector(func(context.Context, Config) (Observation, error) {
			return Observation{}, &ThrottledError{RemoteError: &RemoteError{"Bilibili", "429", true}, After: 120 * time.Second}
		}),
		Observer: reportObserver(func(s Status) {
			if s.State == "retrying" {
				retry = s
				cancel()
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.NextPoll < before.Add(120*time.Second).UnixMilli() {
		t.Fatal("worker ignored upstream Retry-After", retry.NextPoll)
	}
}
