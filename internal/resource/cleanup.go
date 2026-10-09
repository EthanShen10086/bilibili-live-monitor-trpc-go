// Package resource records best-effort cleanup failures without exposing provider data.
package resource

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// LogError is for cleanup that cannot replace an earlier operation error.
func LogError(operation string, err error) {
	if err != nil {
		slog.Warn("cleanup_failed", "operation", operation, "error_type", fmt.Sprintf("%T", err))
	}
}

// Close records cleanup failure; callers must propagate close errors on write paths.
func Close(closer io.Closer) { LogError("close", closer.Close()) }

// Rollback is idempotent after a successful commit or a canceled transaction.
func Rollback(tx *sql.Tx) {
	err := tx.Rollback()
	if !errors.Is(err, sql.ErrTxDone) {
		LogError("rollback", err)
	}
}

// Remove discards only an owned temporary file; missing files are already cleaned.
func Remove(file string) {
	err := os.Remove(file)
	if !errors.Is(err, os.ErrNotExist) {
		LogError("remove_temporary", err)
	}
}
