package monitor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusHeartbeatThrottle(t *testing.T) {
	w := StatusWriter{File: filepath.Join(t.TempDir(), "status.json")}
	s := Status{Running: true, State: "outside_window"}
	now := time.Unix(1000, 0)
	if e := w.Report(&s, now, false); e != nil {
		t.Fatal(e)
	}
	for i := 1; i < 10; i++ {
		if e := w.Report(&s, now.Add(time.Duration(i)*time.Second), false); e != nil {
			t.Fatal(e)
		}
	}
	if w.Writes != 1 {
		t.Fatal(w.Writes)
	}
	w.Report(&s, now.Add(10*time.Second), false)
	s.State = "healthy"
	w.Report(&s, now.Add(11*time.Second), false)
	if w.Writes != 3 {
		t.Fatal(w.Writes)
	}
}
func TestIdleQueueExternalChangesAndExpiry(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.sqlite")
	db, e := OpenStore(file)
	if e != nil {
		t.Fatal(e)
	}
	defer db.DB.Close()
	ext, e := OpenStore(file)
	if e != nil {
		t.Fatal(e)
	}
	defer ext.DB.Close()
	now := time.UnixMilli(100000)
	q := QueueSchedule{Store: db, Dirty: true}
	if e = q.Refresh(now); e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 20; i++ {
		if e = q.Refresh(now.Add(time.Duration(i) * time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	if q.Refreshes != 1 || !q.NextDue.IsZero() {
		t.Fatal(q)
	}
	ext.Observe(Observation{RoomID: 1, Live: true, Title: "test", Start: "2026-10-08T10:00:00.000Z", At: 121000}, false, 1)
	if e = q.Refresh(time.UnixMilli(130000)); e != nil || q.NextDue.UnixMilli() != 121000 {
		t.Fatal(q, e)
	}
	job, e := db.Due(time.UnixMilli(130000))
	if e != nil || job == nil {
		t.Fatal(e)
	}
	db.Failed(job, &RemoteError{"Feishu", "fixed", false}, time.UnixMilli(130000))
	q.Dirty = true
	q.Refresh(time.UnixMilli(130000))
	if !q.NextDue.IsZero() {
		t.Fatal(q)
	}
	ext.Retry(time.UnixMilli(131000))
	q.Refresh(time.UnixMilli(140000))
	if q.NextDue.UnixMilli() != 131000 {
		t.Fatal(q)
	}
	db.DB.Exec("UPDATE jobs SET next=300000 WHERE status='pending'")
	q.Dirty = true
	q.Refresh(time.UnixMilli(140001))
	if q.NextDue.UnixMilli() != 181000 {
		t.Fatal(q)
	}
}
func TestUnchangedObservationDoesNotWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.sqlite")
	db, e := OpenStore(file)
	if e != nil {
		t.Fatal(e)
	}
	defer db.DB.Close()
	observer, e := OpenStore(file)
	if e != nil {
		t.Fatal(e)
	}
	defer observer.DB.Close()
	o := Observation{RoomID: 1, Live: true, Title: "test", Start: "2026-10-08T10:00:00.000Z", At: 1000}
	db.Observe(o, false, 30)
	before, _ := observer.DataVersion()
	o.Title = "changed"
	o.At = 2000
	if added, e := db.Observe(o, false, 30); added || e != nil {
		t.Fatal(added, e)
	}
	after, _ := observer.DataVersion()
	if before != after {
		t.Fatal("unchanged rows rewritten")
	}
}
func TestIdleWorkerStableStatusAndPromptShutdown(t *testing.T) {
	c := testConfig(t)
	c.Schedule.Weekdays = []int{}
	t.Setenv(c.Notification.Group.Webhook, "https://open.feishu.cn/open-apis/bot/v2/hook/test")
	t.Setenv(c.Notification.Group.Secret, "unit")
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, root, c, NewHTTP()) }()
	var first Status
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		first, _ = ReadStatus(root)
		if first.State == "outside_window" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.State != "outside_window" {
		t.Fatal(first)
	}
	stat, _ := os.Stat(filepath.Join(root, "var/status.json"))
	time.Sleep(1200 * time.Millisecond)
	second, _ := ReadStatus(root)
	after, _ := os.Stat(filepath.Join(root, "var/status.json"))
	if second.Updated != first.Updated || second.QueueRefreshes != 1 || !stat.ModTime().Equal(after.ModTime()) {
		t.Fatal("idle writes/scans", first, second)
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked on idle timer")
	}
}
