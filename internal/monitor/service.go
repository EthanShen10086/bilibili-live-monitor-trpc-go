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
	OldestPending         int64  `json:"oldest_pending_at,omitempty"`
	Progress              int64  `json:"last_progress_at,omitempty"`
	Sending               bool   `json:"sending"`
	NotificationState     string `json:"notification_state,omitempty"`
	NotificationError     string `json:"notification_error,omitempty"`
	NotificationFailures  int    `json:"notification_failures"`
	NotificationAttempts  int    `json:"notification_attempts"`
	Storage               string `json:"storage"`
	WorkerRole            string `json:"worker_role"`
	QueueMode             string `json:"queue_mode"`
	Leadership            bool   `json:"leadership"`
	QueueError            string `json:"queue_error,omitempty"`
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
func RunWithDependencies(ctx context.Context, root string, c Config, h *HTTP, deps Dependencies) (runErr error) {
	defaults := defaultDependencies(c, h)
	if deps.Detector == nil {
		deps.Detector = defaults.Detector
	}
	if deps.Notifier == nil {
		deps.Notifier = defaults.Notifier
	}
	if deps.Clock == nil {
		deps.Clock = defaults.Clock
	}
	if deps.OpenRepository == nil {
		deps.OpenRepository = defaults.OpenRepository
	}
	if deps.OpenQueue == nil {
		deps.OpenQueue = defaults.OpenQueue
	}
	nowTime := deps.Clock.Now
	if deps.Observer == nil {
		deps.Observer = noopObserver{}
	}
	deps.Detector = observedDetector{deps.Detector, deps.Observer}
	deps.Notifier = observedNotifier{deps.Notifier, deps.Observer}
	lifeCtx, cancelLife := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelLife()
	defer func() {
		if recover() != nil {
			runErr = fmt.Errorf("worker panicked")
		}
	}()
	if e := c.Platform.Validate(c); e != nil {
		return e
	}
	if e := c.Credentials(); e != nil {
		return e
	}
	release, e := Lock(root, "instance")
	if e != nil {
		return e
	}
	defer release()
	db, e := deps.OpenRepository(lifeCtx, root, c)
	if e != nil {
		return e
	}
	defer db.Close()
	host, _ := os.Hostname()
	s := Status{PID: os.Getpid(), Host: host, Instance: ID(), Running: true, Started: nowTime().UnixMilli(), Mode: c.Detector.Mode, PollingInterval: c.PollingSeconds(), Notification: c.Notification.Mode, State: "starting"}

	s.Storage, s.WorkerRole, s.QueueMode = c.Platform.StorageMode(), c.Platform.WorkerRole(), c.Platform.QueueMode()
	leader, distributed := db.(Leadership)
	var streams TaskQueue
	if c.Platform.QueueMode() == "redis_streams" {
		streams, e = deps.OpenQueue(c, db)
		if e != nil {
			return e
		}
		defer streams.Close()
	}
	var nextStream time.Time
	s.ResourceSafetyVersion = 1
	s.RetentionDays = c.HistoryRetentionDays()
	nextCleanup, e := db.NextCleanupAt(c.HistoryRetentionDays(), nowTime())
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
	if e = queue.Refresh(nowTime()); e != nil {
		return e
	}
	writer := newStatusReporter(filepath.Join(root, "var/status.json"))
	report := func() error {
		s.Pending = queue.Counts
		s.OldestPending = queue.OldestPending
		s.QueueRefreshes = queue.Refreshes
		s.Progress = nowTime().UnixMilli()
		s.Updated = s.Progress
		deps.Observer.Report(s)
		return writer.Report(s, nowTime(), false)
	}
	defer func() {
		s.Running = false
		s.State = "stopped"
		deps.Observer.Report(s)
		if err := writer.Close(s, nowTime()); runErr == nil {
			runErr = err
		}
	}()
	notify := deps.Notifier
	deliveryDone := make(chan deliveryResult, 1)
	var inFlight *Job
	var cancelDelivery context.CancelFunc
	finishDelivery := func(result deliveryResult) error {
		inFlight = nil
		if cancelDelivery != nil {
			cancelDelivery()
		}
		s.Sending = false
		queue.Dirty = true
		if result.err == nil {
			if err := db.Sent(result.job.Key); err != nil {
				return err
			}
			s.LastSent = nowTime().UnixMilli()
			s.NotificationError = ""
			s.NotificationState = "healthy"
			s.NotificationFailures = 0
			Event(root, "notice_sent", map[string]any{"key": result.job.Key, "sent_at": s.LastSent})
		} else {
			if err := db.Failed(result.job, result.err, nowTime()); err != nil {
				return err
			}
			s.NotificationError = result.err.Error()
			s.NotificationState = "blocked"
			if Retryable(result.err) {
				s.NotificationState = "retrying"
			}
			s.NotificationFailures++
			Event(root, "notification_error", s.NotificationError)
		}
		if result.messageID != "" && streams != nil {
			if err := streams.Ack(lifeCtx, result.messageID); err != nil {
				s.QueueError = "Redis acknowledgement unavailable; task state is durable"
			}
		}
		return nil
	}
	defer func() {
		if inFlight != nil {
			var result deliveryResult
			select {
			case result = <-deliveryDone:
			case <-time.After(42 * time.Second):
				cancelDelivery()
				runErr = fmt.Errorf("notification drain timed out")
				return
			}
			if err := finishDelivery(result); runErr == nil {
				runErr = err
			}
			if err := queue.Refresh(nowTime()); runErr == nil {
				runErr = err
			}
			s.Pending = queue.Counts
			s.OldestPending = queue.OldestPending
		}
	}()
	s.NotificationState = "healthy"
	if err := report(); err != nil {
		return err
	}
	var next time.Time
	var currentRoomID int64
	currentInterval := c.PollingSeconds()
	updateCadence := func() (bool, error) {
		if c.Detector.Mode != "polling" || currentRoomID == 0 || !c.InWindow(nowTime()) {
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
		select {
		case result := <-deliveryDone:
			if err := finishDelivery(result); err != nil {
				return err
			}
		default:
		}
		if changed, err := updateCadence(); err != nil {
			return err
		} else if changed {
			next = nowTime().Add(time.Duration(currentInterval) * time.Second)
		}
		now := nowTime()
		window := c.InWindow(now) && c.Platform.WorkerRole() != "sender"
		if distributed && c.Platform.WorkerRole() != "sender" {
			s.Leadership, e = leader.Leadership()
			if e != nil {
				return fmt.Errorf("scheduling leadership unavailable")
			}
			window = window && s.Leadership
		}
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
			if distributed && !s.Leadership && c.Platform.WorkerRole() != "sender" {
				s.State = "standby"
			}
			if c.Platform.WorkerRole() == "sender" {
				s.State = "healthy"
			}
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
					o, err := deps.Detector.Probe(ctx, c)
					if err != nil {
						failures++
						blocked = !Retryable(err)
						s.State = "retrying"
						if blocked {
							s.State = "blocked"
						}
						s.LastError = err.Error()
						next = nowTime().Add(RetryDelay(e, failures, currentInterval))
						Event(root, "detector_error", s.LastError)
					} else {
						if ctx.Err() == nil && c.InWindow(nowTime()) {
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
						next = nowTime().Add(time.Duration(currentInterval) * time.Second)
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
					next = nowTime().Add(Backoff(failures+1, 10))
				}
				if !blocked && official == nil && !now.Before(next) {
					o, err := deps.Detector.Probe(ctx, c)
					if err == nil {
						official = NewOfficial(c, h, o.RoomID)
						err = official.Start(ctx)
					}
					if err == nil && c.InWindow(nowTime()) {
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
						next = nowTime().Add(Backoff(failures, 10))
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
							if c.InWindow(nowTime()) {
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
							next = nowTime().Add(Backoff(failures, 10))
						}
					}
				}
			}
		}
		if !nextCleanup.IsZero() && !nowTime().Before(nextCleanup) {
			removed, err := db.CleanupHistory(c.HistoryRetentionDays(), nowTime())
			if err != nil {
				return err
			}
			queue.Dirty = queue.Dirty || removed > 0
			nextCleanup, e = db.NextCleanupAt(c.HistoryRetentionDays(), nowTime())
			if e != nil {
				return e
			}
			if removed > 0 {
				Event(root, "history_cleaned", removed)
			}
		}
		if e = queue.Refresh(nowTime()); e != nil {
			return e
		}
		var job *Job
		messageID := ""
		if streams != nil && !nowTime().Before(nextStream) {
			nextStream = nowTime().Add(5 * time.Second)
			err := streams.Publish(ctx)
			if err == nil && c.Platform.WorkerRole() != "detector" && inFlight == nil && ctx.Err() == nil {
				job, messageID, err = streams.Receive(ctx)
			}
			if err != nil {
				s.QueueError = "Redis queue unavailable; durable database recovery active"
			} else {
				s.QueueError = ""
			}
			if job != nil {
				queue.Dirty = true
			}
		}
		if job == nil && inFlight == nil && ctx.Err() == nil && c.Platform.WorkerRole() != "detector" && !queue.NextDue.IsZero() && !nowTime().Before(queue.NextDue) {
			job, e = db.Due(nowTime())
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
			sendCtx, cancel := context.WithTimeout(lifeCtx, 40*time.Second)
			cancelDelivery = cancel
			sendKey := job.Key
			if distributed {
				sendKey = c.Platform.SubscriptionID + ":" + job.Key
			}
			inFlight = job
			s.Sending = true
			s.NotificationAttempts++
			go deliver(sendCtx, notify, FormatNotice(n, c.Subscription.RoomID), sendKey, job, messageID, deliveryDone)
		}
		s.NextPoll = 0
		if !next.IsZero() {
			s.NextPoll = next.UnixMilli()
		}
		if e = queue.Refresh(nowTime()); e != nil {
			return e
		}
		if e = report(); e != nil {
			return e
		}
		after := nowTime()
		boundary := after.Truncate(time.Minute).Add(time.Minute)
		queueDeadline := queue.NextDue
		if inFlight != nil {
			queueDeadline = time.Time{}
		}
		if c.Platform.WorkerRole() == "detector" {
			queueDeadline = time.Time{}
		}
		deadline := Earliest(after.Add(WorkerHeartbeat), queue.NextCheck, queueDeadline, nextCleanup, boundary, nextStream)
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
		timer := deps.Clock.NewTimer(max(time.Millisecond, deadline.Sub(nowTime())))
		select {
		case <-ctx.Done():
		case result := <-deliveryDone:
			if err := finishDelivery(result); err != nil {
				timer.Stop()
				return err
			}
		case <-timer.Channel():
		case <-changed:
		case o := <-events:
			if c.InWindow(nowTime()) && ctx.Err() == nil {
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
