package monitor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

// Migration history is global; subscription binding and jobs remain scoped.
func migratePlatform(ctx context.Context, tx *sql.Tx, apply bool) error {
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(platformSchema)))
	if !apply {
		var version int
		var actual string
		if err := tx.QueryRowContext(ctx, "SELECT version,checksum FROM lm_schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version, &actual); err != nil {
			return fmt.Errorf("run platform-migrate before starting")
		}
		if version != 1 || actual != checksum {
			return fmt.Errorf("unsupported or changed platform schema")
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(16160019029)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS lm_schema_migrations(version integer PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT version,checksum FROM lm_schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	applied := false
	for rows.Next() {
		var v int
		var sum string
		if err = rows.Scan(&v, &sum); err != nil {
			resource.Close(rows)
			return err
		}
		if v != 1 || sum != checksum {
			resource.Close(rows)
			return fmt.Errorf("unsupported or changed platform schema")
		}
		applied = true
	}
	err = rows.Err()
	resource.Close(rows)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}
	// Idempotent migration adopts databases created before the ledger existed.
	if _, err = tx.ExecContext(ctx, platformSchema); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO lm_schema_migrations(version,checksum) VALUES(1,$1)", checksum)
	return err
}
