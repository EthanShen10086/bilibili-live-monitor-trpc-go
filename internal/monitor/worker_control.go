package monitor

import (
	"encoding/json"
	"time"
)

const WorkerHeartbeat = 10 * time.Second

type StatusWriter struct {
	File            string
	lastFingerprint string
	LastWrite       time.Time
	Writes          int
}

func (w *StatusWriter) Report(s *Status, now time.Time, force bool) error {
	snapshot := *s
	snapshot.Updated = 0
	// Progress advances on every completed loop, including minute-boundary wakes.
	// Persist it with the heartbeat without making an idle wake a semantic change.
	snapshot.Progress = 0
	b, e := json.Marshal(snapshot)
	if e != nil {
		return e
	}
	if !force && string(b) == w.lastFingerprint && now.Sub(w.LastWrite) < WorkerHeartbeat {
		return nil
	}
	s.Updated = now.UnixMilli()
	if e = AtomicJSON(w.File, s); e != nil {
		return e
	}
	w.lastFingerprint = string(b)
	w.LastWrite = now
	w.Writes++
	return nil
}

type QueueSchedule struct {
	Store              Repository
	Dirty              bool
	NextCheck, NextDue time.Time
	Counts             map[string]int
	Refreshes          int
	OldestPending      int64
	version            int64
}

func (q *QueueSchedule) Refresh(now time.Time) error {
	if !q.Dirty && now.Before(q.NextCheck) {
		return nil
	}
	version, e := q.Store.DataVersion()
	if e != nil {
		return e
	}
	if q.Dirty || version != q.version {
		if q.Counts, e = q.Store.Counts(); e != nil {
			return e
		}
		if q.NextDue, e = q.Store.NextWake(); e != nil {
			return e
		}
		if age, ok := q.Store.(QueueAge); ok {
			if q.OldestPending, e = age.OldestPending(); e != nil {
				return e
			}
		}
		q.Refreshes++
	}
	q.version = version
	q.Dirty = false
	q.NextCheck = now.Add(WorkerHeartbeat)
	return nil
}

func Earliest(fallback time.Time, candidates ...time.Time) time.Time {
	for _, v := range candidates {
		if !v.IsZero() && v.Before(fallback) {
			fallback = v
		}
	}
	return fallback
}
