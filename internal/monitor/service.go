package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Status struct {
	ResourceSafetyVersion int    `json:"resource_safety_version"`
	RetentionDays         int    `json:"history_retention_days"`
	AdaptiveVersion       int    `json:"adaptive_polling_version"`
	EffectiveInterval     int    `json:"effective_polling_interval_seconds"`
	NotifiedLiveInterval  int    `json:"notified_live_interval_seconds"`
	PollingPhase          string `json:"polling_phase"`
	OptimizationVersion   int    `json:"runtime_optimization_version"`
	HeartbeatSeconds      int    `json:"heartbeat_interval_seconds"`
	QueueRefreshes        int    `json:"queue_refreshes"`
	PollingInterval       int    `json:"polling_interval_seconds"`
	NextPoll              int64  `json:"next_poll_at,omitempty"`
	PID                   int    `json:"pid"`
	Host                  string `json:"host"`
	Instance              string `json:"instance"`
	Running               bool   `json:"running"`
	Updated               int64  `json:"updated_at"`
	Started               int64  `json:"started_at"`
	Mode                  string `json:"mode"`
	Notification          string `json:"notification"`
	State                 string `json:"detector_state"`
	InWindow              bool   `json:"in_window"`
	LastObservation       int64  `json:"last_observation_at,omitempty"`
	LastSent              int64  `json:"last_sent_at,omitempty"`
	LastError             string `json:"last_error,omitempty"`
	Pending               any    `json:"pending"`
}

func ReadStatus(root string) (Status, error) {
	var s Status
	b, e := os.ReadFile(filepath.Join(root, "var/status.json"))
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	return s, e
}
func Run(ctx context.Context, root string, c Config, h *HTTP) error {
	if e := c.Credentials(); e != nil {
		return e
	}
	release, e := Lock(root, "instance")
	if e != nil {
		return e
	}
	defer release()
	db, e := OpenStore(filepath.Join(root, "var/state.sqlite"))
	if e != nil {
		return e
	}
	defer db.DB.Close()
	host, _ := os.Hostname()
	s := Status{PID: os.Getpid(), Host: host, Instance: ID(), Running: true, Started: time.Now().UnixMilli(), Mode: c.Detector.Mode, PollingInterval: c.PollingSeconds(), Notification: c.Notification.Mode, State: "starting"}

	s.ResourceSafetyVersion = 1
	s.RetentionDays = c.HistoryRetentionDays()
	nextCleanup, e := db.NextCleanupAt(c.HistoryRetentionDays(), time.Now())
	if e != nil {
		return e
	}
	s.AdaptiveVersion = 1
	s.EffectiveInterval = c.PollingSeconds()
	s.NotifiedLiveInterval = c.NotifiedLiveSeconds()
	s.PollingPhase = "awaiting_start"
	if c.Detector.Mode == "official" {
		s.PollingPhase = "official"
	}
	s.OptimizationVersion = 1
	s.HeartbeatSeconds = int(WorkerHeartbeat / time.Second)
	queue := QueueSchedule{Store: db, Dirty: true}
	if e = queue.Refresh(time.Now()); e != nil {
		return e
	}
	writer := StatusWriter{File: filepath.Join(root, "var/status.json")}
	report := func() error {
		s.Pending = queue.Counts
		s.QueueRefreshes = queue.Refreshes
		return writer.Report(&s, time.Now(), false)
	}
	defer func() { s.Running = false; s.State = "stopped"; writer.Report(&s, time.Now(), true) }()
	notify := Feishu{Config: c, HTTP: h}
	var next time.Time
	var currentRoomID int64
	currentInterval := c.PollingSeconds()
	updateCadence := func() (bool, error) {
		if c.Detector.Mode != "polling" || currentRoomID == 0 || !c.InWindow(time.Now()) {
			return false, nil
		}
		phase, err := db.PollingPhase(currentRoomID)
		if err != nil {
			return false, err
		}
		interval := c.PollingSeconds()
		if phase == "notified_live" {
			interval = c.NotifiedLiveSeconds()
		}
		changed := interval != currentInterval
		currentInterval = interval
		s.PollingPhase, s.EffectiveInterval = phase, interval
		return changed, nil
	}
	failures := 0
	wasWindow := false
	catchup := true
	blocked := false
	var official *Official
	cleanup := func() error {
		if official == nil {
			return nil
		}
		cc, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := official.Stop(cc); err != nil {
			return err
		}
		official = nil
		return nil
	}
	defer cleanup()
	for ctx.Err() == nil {
		now := time.Now()
		window := c.InWindow(now)
		s.InWindow = window
		if !window {
			if e = cleanup(); e != nil {
				s.State = "session_cleanup_failed"
				s.LastError = e.Error()
				if e = report(); e != nil {
					return e
				}
				Pause(ctx, WorkerHeartbeat)
				continue
			}
			s.State = "outside_window"
			s.PollingPhase = "outside_window"
			wasWindow = false
			catchup = true
			next = time.Time{}
		} else {
			if !wasWindow {
				catchup = true
				next = time.Time{}
				if c.Detector.Mode == "polling" {
					currentInterval = c.PollingSeconds()
					s.PollingPhase = "awaiting_start"
					s.EffectiveInterval = currentInterval
				}
			}
			wasWindow = true
			if c.Detector.Mode == "polling" {
				if !blocked && !now.Before(next) {
					o, err := h.Probe(ctx, c)
					if err != nil {
						failures++
						blocked = !Retryable(err)
						s.State = "retrying"
						if blocked {
							s.State = "blocked"
						}
						s.LastError = err.Error()
						next = time.Now().Add(Backoff(failures, currentInterval))
						Event(root, "detector_error", s.LastError)
					} else {
						if ctx.Err() == nil && c.InWindow(time.Now()) {
							var added bool
							added, err = db.Observe(o, catchup, c.Notification.TTL)
							if err != nil {
								return err
							}
							catchup = false
							s.LastObservation = o.At
							queue.Dirty = queue.Dirty || added
							currentRoomID = o.RoomID
							if _, e = updateCadence(); e != nil {
								return e
							}
						}
						failures = 0
						s.State = "healthy"
						s.LastError = ""
						next = time.Now().Add(time.Duration(currentInterval) * time.Second)
					}
				}
			} else {
				if s.State == "session_cleanup_failed" && official != nil {
					if ce := cleanup(); ce != nil {
						s.LastError = ce.Error()
						if e = report(); e != nil {
							return e
						}
						if e = Pause(ctx, WorkerHeartbeat); e != nil {
							break
						}
						continue
					}
					s.State = "retrying"
					if blocked {
						s.State = "blocked"
					}
					next = time.Now().Add(Backoff(failures+1, 10))
				}
				if !blocked && official == nil && !now.Before(next) {
					o, err := h.Probe(ctx, c)
					if err == nil {
						official = NewOfficial(c, h, o.RoomID)
						err = official.Start(ctx)
					}
					if err == nil && c.InWindow(time.Now()) {
						_, err = db.Observe(o, true, c.Notification.TTL)
						s.LastObservation = o.At
						queue.Dirty = true
					}
					if err != nil {
						blocked = !Retryable(err)
						if ce := cleanup(); ce != nil {
							s.State = "session_cleanup_failed"
							s.LastError = ce.Error()
							if e = report(); e != nil {
								return e
							}
							Pause(ctx, WorkerHeartbeat)
							continue
						}
						failures++
						blocked = !Retryable(err)
						s.State = "retrying"
						if blocked {
							s.State = "blocked"
						}
						s.LastError = err.Error()
						next = time.Now().Add(Backoff(failures, 10))
					} else {
						failures = 0
						s.State = "healthy"
						s.LastError = ""
					}
				}
				if official != nil {
					err := official.Tick(ctx)
					for err == nil {
						select {
						case o := <-official.Events:
							if c.InWindow(time.Now()) {
								_, err = db.Observe(o, false, c.Notification.TTL)
								s.LastObservation = o.At
								queue.Dirty = true
							}
						default:
							goto drained
						}
					}
				drained:
					if err != nil {
						blocked = !Retryable(err)
						if ce := cleanup(); ce != nil {
							s.State = "session_cleanup_failed"
							s.LastError = ce.Error()
						} else {
							failures++
							blocked = !Retryable(err)
							s.State = "retrying"
							if blocked {
								s.State = "blocked"
							}
							s.LastError = err.Error()
							next = time.Now().Add(Backoff(failures, 10))
						}
					}
				}
			}
		}
		if !nextCleanup.IsZero() && !time.Now().Before(nextCleanup) {
			removed, err := db.CleanupHistory(c.HistoryRetentionDays(), time.Now())
			if err != nil {
				return err
			}
			queue.Dirty = queue.Dirty || removed > 0
			nextCleanup, e = db.NextCleanupAt(c.HistoryRetentionDays(), time.Now())
			if e != nil {
				return e
			}
			if removed > 0 {
				Event(root, "history_cleaned", removed)
			}
		}
		if e = queue.Refresh(time.Now()); e != nil {
			return e
		}
		var job *Job
		if !queue.NextDue.IsZero() && !time.Now().Before(queue.NextDue) {
			job, e = db.Due(time.Now())
			if e != nil {
				return e
			}
			queue.Dirty = true
		}
		if job != nil {
			var n Notice
			if json.Unmarshal([]byte(job.Payload), &n) != nil {
				return fmt.Errorf("invalid durable notification payload")
			}
			err := notify.Send(ctx, FormatNotice(n, c.Subscription.RoomID), job.Key)
			if err == nil {
				if e = db.Sent(job.Key); e != nil {
					return e
				}
				changed, ce := updateCadence()
				if ce != nil {
					return ce
				}
				if changed && window {
					next = time.Now().Add(time.Duration(currentInterval) * time.Second)
				}
				s.LastSent = time.Now().UnixMilli()
				Event(root, "notice_sent", map[string]any{"key": job.Key, "sent_at": s.LastSent})
			} else {
				if e = db.Failed(job, err, time.Now()); e != nil {
					return e
				}
				Event(root, "notification_error", err.Error())
			}
		}
		s.NextPoll = 0
		if !next.IsZero() {
			s.NextPoll = next.UnixMilli()
		}
		if e = queue.Refresh(time.Now()); e != nil {
			return e
		}
		if e = report(); e != nil {
			return e
		}
		after := time.Now()
		boundary := after.Truncate(time.Minute).Add(time.Minute)
		deadline := Earliest(writer.LastWrite.Add(WorkerHeartbeat), queue.NextCheck, queue.NextDue, nextCleanup, boundary)
		if window && !blocked && (c.Detector.Mode == "polling" || official == nil) {
			deadline = Earliest(deadline, next)
		}
		var events <-chan Observation
		var changed <-chan struct{}
		if official != nil && s.State != "session_cleanup_failed" {
			official.mu.Lock()
			ack := official.lastAck
			official.mu.Unlock()
			deadline = Earliest(deadline, official.lastGame.Add(20*time.Second), ack.Add(45*time.Second+time.Millisecond))
			events = official.Events
			changed = official.Changed
		}
		timer := time.NewTimer(max(time.Millisecond, time.Until(deadline)))
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-changed:
		case o := <-events:
			if c.InWindow(time.Now()) && ctx.Err() == nil {
				if _, e = db.Observe(o, false, c.Notification.TTL); e != nil {
					timer.Stop()
					return e
				}
				s.LastObservation = o.At
				queue.Dirty = true
			}
		}
		timer.Stop()
	}
	return nil
}
