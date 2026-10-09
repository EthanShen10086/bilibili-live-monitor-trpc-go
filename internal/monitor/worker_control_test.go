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

func TestAdaptivePollingCurrentSentSession(t *testing.T) {
	c := testConfig(t)
	if c.NotifiedLiveSeconds() != 300 {
		t.Fatal(c.NotifiedLiveSeconds())
	}
	ten := 10
	c.Detector.Polling.IntervalMinutes = &ten
	if c.NotifiedLiveSeconds() != 600 {
		t.Fatal("slowing must not increase frequency")
	}
	zero := 0
	c.Detector.Polling.NotifiedLiveMinutes = &zero
	if c.Validate() == nil {
		t.Fatal("invalid adaptive interval accepted")
	}
	db, e := OpenStore(filepath.Join(t.TempDir(), "state.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.DB.Close()
	check := func(expected string) {
		t.Helper()
		phase, e := db.PollingPhase(1)
		if e != nil || phase != expected {
			t.Fatal(phase, e)
		}
	}
	check("awaiting_start")
	o := Observation{RoomID: 1, Live: true, Start: "2026-10-08T10:00:00.000Z", At: 1000}
	if _, e = db.Observe(o, true, 30); e != nil {
		t.Fatal(e)
	}
	check("awaiting_notification")
	job, e := db.Due(time.UnixMilli(1001))
	if e != nil || job == nil {
		t.Fatal(e)
	}
	if e = db.Failed(job, &RemoteError{"Feishu", "fail", false}, time.UnixMilli(1002)); e != nil {
		t.Fatal(e)
	}
	check("awaiting_notification")
	if e = db.Sent(job.Key); e != nil {
		t.Fatal(e)
	}
	check("notified_live")
	o.Start = "2026-10-08T11:00:00.000Z"
	o.At = 2000
	if _, e = db.Observe(o, false, 30); e != nil {
		t.Fatal(e)
	}
	check("awaiting_notification")
	o.Live = false
	o.At = 3000
	if _, e = db.Observe(o, false, 30); e != nil {
		t.Fatal(e)
	}
	check("awaiting_start")
}

func TestDeclinedApprovalIdleAndCancellation(t *testing.T) {
	c := testConfig(t)
	root := t.TempDir()
	if e := AtomicJSON(filepath.Join(root, "var/boot-approval.json"), Approval{"test", "declined", time.Now().UnixMilli()}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- awaitApproval(ctx, root, c, "test") }()
	var first Status
	for i := 0; i < 100; i++ {
		first, _ = ReadStatus(root)
		if first.State == "waiting_confirmation" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.State != "waiting_confirmation" {
		t.Fatal(first)
	}
	time.Sleep(1200 * time.Millisecond)
	second, _ := ReadStatus(root)
	if first.Updated != second.Updated {
		t.Fatal("waiting approval rewrote status")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("approval did not stop")
	}
}

func TestApprovedBootDoesNotOverwriteWorkerStatus(t *testing.T) {
	root := t.TempDir()
	c := testConfig(t)
	if e := AtomicJSON(filepath.Join(root, "var/boot-approval.json"), Approval{"test", "approved", 1000}); e != nil {
		t.Fatal(e)
	}
	original := Status{PID: 999, Running: true, State: "healthy", Updated: 1000}
	if e := AtomicJSON(filepath.Join(root, "var/status.json"), original); e != nil {
		t.Fatal(e)
	}
	if e := awaitApproval(context.Background(), root, c, "test"); e != nil {
		t.Fatal(e)
	}
	actual, e := ReadStatus(root)
	if e != nil || actual.PID != original.PID || actual.State != original.State || actual.Updated != original.Updated {
		t.Fatal(actual, e)
	}
}
