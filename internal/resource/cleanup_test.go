package resource

import (
	"bytes"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type failingCloser struct{ calls int }

func (closer *failingCloser) Close() error {
	closer.calls++
	return errors.New("private-provider-token")
}

func TestCleanupLogsErrorTypeWithoutSecrets(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	closer := &failingCloser{}
	Close(closer)
	if closer.calls != 1 || !strings.Contains(output.String(), "cleanup_failed") || strings.Contains(output.String(), "private-provider-token") {
		t.Fatal("cleanup error lost or exposed secrets")
	}
}

func TestAlreadyCompletedCleanupDoesNotWarn(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	Rollback(tx)
	Remove(t.TempDir() + "/already-missing")
	LogError("close", nil)
	if output.Len() != 0 {
		t.Fatal("successful cleanup produces false warnings", output.String())
	}
}
