package monitor

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkSQLiteUnchangedObservation(b *testing.B) {
	db, err := OpenStore(filepath.Join(b.TempDir(), "state.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.DB.Close() })
	observation := Observation{RoomID: 1616, Live: true, Start: "2026-10-09T10:00:00Z", At: time.Now().UnixMilli()}
	if _, err = db.Observe(observation, false, 30); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if added, err := db.Observe(observation, false, 30); added || err != nil {
			b.Fatal("unchanged observation created a task", added, err)
		}
	}
}

func BenchmarkSQLiteQueueCounts10000(b *testing.B) {
	db, err := OpenStore(filepath.Join(b.TempDir(), "state.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.DB.Close() })
	tx, err := db.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := range 10000 {
		state := "pending"
		if i%2 == 0 {
			state = "sent"
		}
		if _, err = tx.Exec("INSERT INTO jobs(key,payload,status,next,expires) VALUES(?,?,?,?,?)", fmt.Sprintf("bench-%d", i), "{}", state, 0, 0); err != nil {
			_ = tx.Rollback()
			b.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		counts, err := db.Counts()
		if err != nil || counts["pending"] != 5000 || counts["sent"] != 5000 {
			b.Fatal("incorrect queue aggregation", counts, err)
		}
	}
}
