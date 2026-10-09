package monitor

import (
	"context"
	"fmt"
	"time"
)

// Detector observes upstream state without exposing provider protocols to the worker.
type Detector interface {
	Probe(context.Context, Config) (Observation, error)
}
type Notifier interface {
	Send(context.Context, string, string) error
}
type (
	Leadership interface{ Leadership() (bool, error) }
	Timer      interface {
		Channel() <-chan time.Time
		Stop() bool
	}
)

type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type (
	realClock struct{}
	realTimer struct{ *time.Timer }
)

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
func (t realTimer) Channel() <-chan time.Time    { return t.C }

type Dependencies struct {
	Detector       Detector
	Notifier       Notifier
	Clock          Clock
	OpenRepository func(context.Context, string, Config) (Repository, error)
	OpenQueue      func(Config, Repository) (TaskQueue, error)
	Observer       Observer
}

type Observer interface {
	Begin(context.Context, string) (context.Context, func(error))
	Report(Status)
}
type noopObserver struct{}

func (noopObserver) Begin(ctx context.Context, _ string) (context.Context, func(error)) {
	return ctx, func(error) {}
}
func (noopObserver) Report(Status) {}

type observedDetector struct {
	Detector
	observer Observer
}

func (d observedDetector) Probe(ctx context.Context, c Config) (o Observation, err error) {
	ctx, end := d.observer.Begin(ctx, "detector")
	defer func() { end(err) }()
	return d.Detector.Probe(ctx, c)
}

type observedNotifier struct {
	Notifier
	observer Observer
}

func (n observedNotifier) Send(ctx context.Context, text, key string) (err error) {
	ctx, end := n.observer.Begin(ctx, "notification")
	defer func() { end(err) }()
	return n.Notifier.Send(ctx, text, key)
}

func defaultDependencies(c Config, h *HTTP) Dependencies {
	return Dependencies{Detector: h, Notifier: &Feishu{Config: c, HTTP: h}, Clock: realClock{}, OpenRepository: OpenRepository, OpenQueue: func(c Config, db Repository) (TaskQueue, error) {
		pg, ok := db.(*PostgresStore)
		if !ok {
			return nil, fmt.Errorf("redis streams requires a PostgreSQL repository")
		}
		return OpenStreamQueue(c, pg)
	}}
}

func Run(ctx context.Context, root string, c Config, h *HTTP) error {
	defer h.Client.CloseIdleConnections()
	return RunWithDependencies(ctx, root, c, h, defaultDependencies(c, h))
}

type deliveryResult struct {
	job       *Job
	messageID string
	err       error
}

// Delivery has its own bounded lifetime. Cancellation stops acquisition, not an accepted send.
func deliver(ctx context.Context, notify Notifier, text, key string, job *Job, messageID string, done chan<- deliveryResult) {
	result := deliveryResult{job: job, messageID: messageID}
	defer func() {
		if recover() != nil {
			result.err = &RemoteError{"Notifier", "panic", false}
		}
		done <- result
	}()
	result.err = notify.Send(ctx, text, key)
}
