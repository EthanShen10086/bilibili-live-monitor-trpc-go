package monitor

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type testDetector func(context.Context, Config) (Observation, error)

func (f testDetector) Probe(ctx context.Context, c Config) (Observation, error) { return f(ctx, c) }

type testNotifier func(context.Context, string, string) error

func (f testNotifier) Send(ctx context.Context, text, key string) error { return f(ctx, text, key) }
func activeConfig(t *testing.T) Config {
	c := testConfig(t)
	c.Schedule.Weekdays = []int{1, 2, 3, 4, 5, 6, 7}
	c.Schedule.Start = "00:00"
	c.Schedule.End = "24:00"
	t.Setenv(c.Notification.Group.Webhook, "https://open.feishu.cn/open-apis/bot/v2/hook/fake")
	t.Setenv(c.Notification.Group.Secret, "fake")
	return c
}

func TestSlowDeliveryDoesNotBlockDetectorAndDrainsOnStop(t *testing.T) {
	c := activeConfig(t)
	one := 1
	c.Detector.Polling.IntervalMinutes = nil
	c.Detector.Polling.Interval = &one
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	var repositoryContext context.Context
	deps := Dependencies{
		Detector: testDetector(func(context.Context, Config) (Observation, error) {
			probes.Add(1)
			return Observation{RoomID: 11163068, Live: true, Start: "session", At: time.Now().UnixMilli()}, nil
		}),
		Notifier: testNotifier(func(ctx context.Context, _, _ string) error {
			close(started)
			select {
			case <-release:
				return ctx.Err()
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
		OpenRepository: func(ctx context.Context, root string, c Config) (Repository, error) {
			repositoryContext = ctx
			return OpenRepository(ctx, root, c)
		},
	}
	done := make(chan error, 1)
	go func() { done <- RunWithDependencies(ctx, root, c, NewHTTP(), deps) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery")
	}
	deadline := time.Now().Add(3 * time.Second)
	for probes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if probes.Load() < 2 {
		t.Fatal("notification blocked detection")
	}
	s, err := ReadStatus(root)
	if err != nil || !s.Live(time.Now()) || !s.Sending {
		t.Fatal(s, err)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatal("returned before accepted delivery drained", err)
	case <-time.After(30 * time.Millisecond):
	}
	if repositoryContext.Err() != nil {
		t.Fatal("stop cancelled durable completion")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain blocked")
	}
	db, err := OpenStore(filepath.Join(root, "var/state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts, err := db.Counts()
	if err != nil || counts["sent"] != 1 || counts["pending"] != 0 {
		t.Fatal(counts, err)
	}
}

func TestPermanentFailureDegradesBusinessHealthButKeepsProcessLive(t *testing.T) {
	c := activeConfig(t)
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunWithDependencies(ctx, root, c, NewHTTP(), Dependencies{
			Detector: testDetector(func(context.Context, Config) (Observation, error) {
				return Observation{RoomID: 11163068, Live: true, Start: "session", At: time.Now().UnixMilli()}, nil
			}),
			Notifier: testNotifier(func(context.Context, string, string) error { return &RemoteError{"Feishu", "19024", false} }),
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := ReadStatus(root)
		if err == nil && s.NotificationState == "blocked" {
			if s.BusinessHealthy(time.Now()) || !s.Live(time.Now()) || s.Ready(time.Now()) {
				t.Fatal("incorrect probe semantics", s)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("notification failure not exposed")
}

func TestNotifierPanicBecomesDurableFailure(t *testing.T) {
	result := make(chan deliveryResult, 1)
	go deliver(context.Background(), testNotifier(func(context.Context, string, string) error { panic("provider panic") }), "", "", &Job{Key: "test"}, "", result)
	select {
	case r := <-result:
		if r.err == nil || Retryable(r.err) {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("panic stranded claim")
	}
}
