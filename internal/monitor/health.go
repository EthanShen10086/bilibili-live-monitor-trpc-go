package monitor

import "time"

func (s Status) Live(now time.Time) bool {
	age := now.UnixMilli() - s.Updated
	progress := s.Progress
	if progress == 0 {
		progress = s.Updated
	} // Old local status snapshots remain readable.
	return s.Running && age >= -2000 && age <= 20000 && now.UnixMilli()-progress <= 90000
}
func (s Status) Ready(now time.Time) bool {
	return s.Live(now) && s.State != "starting" && s.State != "stopped" && s.State != "blocked" && s.State != "session_cleanup_failed" && s.NotificationState != "blocked"
}
func (s Status) BusinessHealthy(now time.Time) bool {
	if !s.Ready(now) || s.State == "retrying" || s.QueueError != "" {
		return false
	}
	if s.WorkerRole == "detector" {
		return true
	}
	if s.NotificationState == "retrying" || s.NotificationError != "" {
		return false
	}
	switch counts := s.Pending.(type) {
	case map[string]int:
		return counts["failed"] == 0
	case map[string]interface{}:
		if n, ok := counts["failed"].(float64); ok {
			return n == 0
		}
	}
	return true
}
