package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

// VACUUM INTO takes a consistent SQLite snapshot, including committed journal data.
// Reserving an empty destination with O_EXCL prevents overwriting another backup.
func snapshotSQLite(ctx context.Context, source, destination string) (err error) {
	if !filepath.IsAbs(destination) {
		return fmt.Errorf("backup destination must be absolute")
	}
	if info, e := os.Stat(source); e != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("source database unavailable")
	}
	uri := url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro"}
	db, e := sql.Open("sqlite", uri.String())
	if e != nil {
		return e
	}
	defer resource.Close(db)
	cc, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var check string
	if e = db.QueryRowContext(cc, "PRAGMA quick_check").Scan(&check); e != nil || check != "ok" {
		return fmt.Errorf("source database integrity check failed")
	}
	// Require monitor tables, rather than accepting an unrelated SQLite database.
	var count int
	if e = db.QueryRowContext(cc, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('observations','jobs','maintenance')").Scan(&count); e != nil || count != 3 {
		return fmt.Errorf("source is not a monitor database")
	}
	f, e := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if e != nil {
		return e
	}
	if err = f.Close(); err != nil {
		resource.Remove(destination)
		return err
	}
	defer func() {
		if err != nil {
			resource.Remove(destination)
		}
	}()
	_, err = db.ExecContext(cc, "VACUUM INTO ?", destination)
	return err
}

func BackupSQLite(ctx context.Context, root, destination string) error {
	return snapshotSQLite(ctx, filepath.Join(root, "var/state.sqlite"), destination)
}

func RestoreSQLite(ctx context.Context, root, source string) error {
	if !filepath.IsAbs(source) {
		return fmt.Errorf("restore source must be absolute")
	}
	if err := AssertStopped(root); err != nil {
		return err
	}
	release, err := Lock(root, "instance")
	if err != nil {
		return err
	}
	defer release()
	return snapshotSQLite(ctx, source, filepath.Join(root, "var/state.sqlite"))
}
