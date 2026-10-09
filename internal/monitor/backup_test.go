package monitor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOnlineBackupAndStoppedRestorePreserveDeduplication(t *testing.T) {
	root := t.TempDir()
	db, err := OpenStore(filepath.Join(root, "var/state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	observation := Observation{RoomID: 1, Live: true, Start: "session", At: time.Now().UnixMilli()}
	if _, err = db.Observe(observation, false, 30); err != nil {
		t.Fatal(err)
	}
	oldest, err := db.OldestPending()
	if err != nil || oldest != observation.At {
		t.Fatal(oldest, err)
	}
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	if err = BackupSQLite(context.Background(), root, backup); err != nil {
		t.Fatal(err)
	}
	if err = BackupSQLite(context.Background(), root, backup); err == nil {
		t.Fatal("overwrote backup")
	}
	if info, _ := os.Stat(backup); info.Mode().Perm() != 0o600 {
		t.Fatal("backup permissions")
	}
	target := t.TempDir()
	if err = RestoreSQLite(context.Background(), target, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(filepath.Join(target, "var/state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if added, err := restored.Observe(observation, true, 30); err != nil || added {
		t.Fatal("lost deduplication", err)
	}
	counts, _ := restored.Counts()
	if counts["pending"] != 1 {
		t.Fatal(counts)
	}
	if err = RestoreSQLite(context.Background(), target, backup); err == nil {
		t.Fatal("overwrote active state")
	}
	release, err := Lock(root, "instance")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = RestoreSQLite(context.Background(), root, backup); err == nil {
		t.Fatal("restored running instance")
	}
}
