package monitor

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyHistoryPreservesCurrentAndPending(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.sqlite")
	db, e := OpenStore(file)
	if e != nil {
		t.Fatal(e)
	}
	defer db.DB.Close()
	now := time.UnixMilli(100 * 24 * 60 * 60 * 1000)
	if _, e = db.Observe(Observation{RoomID: 1, Live: true, Start: "2026-10-08T10:00:00Z", At: 1000}, true, 30); e != nil {
		t.Fatal(e)
	}
	job, e := db.Due(time.UnixMilli(1001))
	if e != nil || job == nil {
		t.Fatal(e)
	}
	if e = db.Sent(job.Key); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec("UPDATE jobs SET expires=0"); e != nil {
		t.Fatal(e)
	}
	tx, e := db.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	stmt, e := tx.Prepare("INSERT INTO jobs(key,payload,status,next,expires) VALUES(?,'{}',?,0,?)")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 205; i++ {
		if _, e = stmt.Exec(fmt.Sprint("old", i), []string{"sent", "expired", "failed"}[i%3], 0); e != nil {
			t.Fatal(e)
		}
	}
	stmt.Exec("pending", "pending", 0)
	stmt.Exec("recent", "sent", now.UnixMilli())
	stmt.Close()
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if n, e := db.CleanupHistory(90, now); n != 200 || e != nil {
		t.Fatal(n, e)
	}
	if n, e := db.CleanupHistory(90, now.Add(time.Second)); n != 0 || e != nil {
		t.Fatal(n, e)
	}
	if due, e := db.NextCleanupAt(90, now.Add(time.Second)); e != nil || !due.Equal(now.Add(24*time.Hour)) {
		t.Fatal(due, e)
	}
	if n, e := db.CleanupHistory(90, now.Add(24*time.Hour)); n != 5 || e != nil {
		t.Fatal(n, e)
	}
	if phase, e := db.PollingPhase(1); e != nil || phase != "notified_live" {
		t.Fatal(phase, e)
	}
	var count int
	if e = db.DB.QueryRow("SELECT count(*) FROM jobs").Scan(&count); e != nil || count != 3 {
		t.Fatal(count, e)
	}
	reopened, e := OpenStore(file)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.DB.Close()
	if phase, e := reopened.PollingPhase(1); e != nil || phase != "notified_live" {
		t.Fatal(phase, e)
	}
	if n, e := db.CleanupHistory(0, now.Add(10*24*time.Hour)); n != 0 || e != nil {
		t.Fatal(n, e)
	}
	if due, e := db.NextCleanupAt(0, now); !due.IsZero() || e != nil {
		t.Fatal(due, e)
	}
}
