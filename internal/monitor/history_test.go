package monitor

import (
	"fmt"
	"path/filepath"
	"strings"
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

func TestPendingIndexesAndExpiry(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	tx, err := db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("INSERT INTO jobs(key,payload,status,next,expires) VALUES(?,'{}',?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < 2000; i++ {
		if _, err = stmt.Exec(fmt.Sprint("sent", i), "sent", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []struct {
		k    string
		n, e int
	}{{"due", 10, 100}, {"expired", 0, 5}, {"future", 200, 300}} {
		if _, err = stmt.Exec(r.k, "pending", r.n, r.e); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct{ sql, index string }{
		{"SELECT key FROM jobs WHERE status='pending' AND next<=20 ORDER BY next LIMIT 1", "jobs_pending_next"},
		{"UPDATE jobs SET status='expired' WHERE status='pending' AND expires<=20", "jobs_pending_expires"},
	} {
		rows, err := db.DB.Query("EXPLAIN QUERY PLAN " + q.sql)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for rows.Next() {
			var a, b, c int
			var detail string
			if err = rows.Scan(&a, &b, &c, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if strings.Contains(detail, q.index) {
				found = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil || !found {
			t.Fatalf("index %s unused: %v", q.index, err)
		}
	}
	job, err := db.Due(time.UnixMilli(20))
	if err != nil || job == nil || job.Key != "due" {
		t.Fatalf("due: %v %v", job, err)
	}
	if err = db.Sent("due"); err != nil {
		t.Fatal(err)
	}
	next, err := db.NextWake()
	if err != nil || next.UnixMilli() != 200 {
		t.Fatalf("next: %v %v", next, err)
	}
	var state string
	if err = db.DB.QueryRow("SELECT status FROM jobs WHERE key='expired'").Scan(&state); err != nil || state != "expired" {
		t.Fatalf("expiry: %s %v", state, err)
	}
	reopened, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	reopened.DB.Close()
	var count int
	if err = db.DB.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 2003 {
		t.Fatalf("migration: %d %v", count, err)
	}
}
