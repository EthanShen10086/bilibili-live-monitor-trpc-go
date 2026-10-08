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
	PollingInterval int    `json:"polling_interval_seconds"`
	NextPoll        int64  `json:"next_poll_at,omitempty"`
	PID             int    `json:"pid"`
	Host            string `json:"host"`
	Instance        string `json:"instance"`
	Running         bool   `json:"running"`
	Updated         int64  `json:"updated_at"`
	Started         int64  `json:"started_at"`
	Mode            string `json:"mode"`
	Notification    string `json:"notification"`
	State           string `json:"detector_state"`
	InWindow        bool   `json:"in_window"`
	LastObservation int64  `json:"last_observation_at,omitempty"`
	LastSent        int64  `json:"last_sent_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	Pending         any    `json:"pending"`
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
	report := func() error {
		s.Updated = time.Now().UnixMilli()
		s.Pending, e = db.Counts()
		if e != nil {
			return e
		}
		return AtomicJSON(filepath.Join(root, "var/status.json"), s)
	}
	defer func() { s.Running = false; s.State = "stopped"; report() }()
	notify := Feishu{Config: c, HTTP: h}
	var next time.Time
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
				Pause(ctx, time.Second)
				continue
			}
			s.State = "outside_window"
			wasWindow = false
			catchup = true
			next = time.Time{}
		} else {
			if !wasWindow {
				catchup = true
				next = time.Time{}
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
						next = time.Now().Add(Backoff(failures, c.PollingSeconds()))
						Event(root, "detector_error", s.LastError)
					} else {
						if ctx.Err() == nil && c.InWindow(time.Now()) {
							_, err = db.Observe(o, catchup, c.Notification.TTL)
							if err != nil {
								return err
							}
							catchup = false
							s.LastObservation = o.At
						}
						failures = 0
						s.State = "healthy"
						s.LastError = ""
						next = time.Now().Add(time.Duration(c.PollingSeconds()) * time.Second)
					}
				}
			} else {
				if s.State == "session_cleanup_failed" && official != nil {
					if ce := cleanup(); ce != nil {
						s.LastError = ce.Error()
						if e = report(); e != nil {
							return e
						}
						if e = Pause(ctx, time.Second); e != nil {
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
					}
					if err != nil {
						blocked = !Retryable(err)
						if ce := cleanup(); ce != nil {
							s.State = "session_cleanup_failed"
							s.LastError = ce.Error()
							if e = report(); e != nil {
								return e
							}
							Pause(ctx, time.Second)
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
		job, err := db.Due(time.Now())
		if err != nil {
			return err
		}
		if job != nil {
			var n Notice
			if json.Unmarshal([]byte(job.Payload), &n) != nil {
				return fmt.Errorf("invalid durable notification payload")
			}
			err = notify.Send(ctx, FormatNotice(n, c.Subscription.RoomID), job.Key)
			if err == nil {
				if e = db.Sent(job.Key); e != nil {
					return e
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
		if e = report(); e != nil {
			return e
		}
		if e = Pause(ctx, time.Second); e != nil {
			break
		}
	}
	return nil
}
