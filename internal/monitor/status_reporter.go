package monitor

import (
	"sync"
	"time"
)

// The heartbeat reports a synchronized snapshot; business progress is tracked separately.
type statusReporter struct {
	mu         sync.Mutex
	writer     StatusWriter
	snapshot   Status
	err        error
	stop, done chan struct{}
}

func newStatusReporter(file string) *statusReporter {
	r := &statusReporter{writer: StatusWriter{File: file}, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		tick := time.NewTicker(WorkerHeartbeat)
		defer tick.Stop()
		for {
			select {
			case <-r.stop:
				return
			case now := <-tick.C:
				r.mu.Lock()
				if r.snapshot.Running && r.err == nil {
					r.err = r.writer.Report(&r.snapshot, now, false)
				}
				r.mu.Unlock()
			}
		}
	}()
	return r
}

func (r *statusReporter) Report(s Status, now time.Time, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.snapshot = s
	return r.writer.Report(&r.snapshot, now, force)
}

func (r *statusReporter) Close(s Status, now time.Time) error {
	close(r.stop)
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot = s
	return r.writer.Report(&r.snapshot, now, true)
}
