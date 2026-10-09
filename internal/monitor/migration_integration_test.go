package monitor

import (
	"context"
	"testing"
)

func TestPostgresMigrationLedgerAndStrictStartup(t *testing.T) {
	c := platformConfig(t)
	s := platformStore(t, c)
	clearScope(t, s)
	var checksum string
	if err := s.DB.QueryRow("SELECT checksum FROM lm_schema_migrations WHERE version=1").Scan(&checksum); err != nil || len(checksum) != 64 {
		t.Fatal(checksum, err)
	}
	no := false
	c.Platform.Postgres.AutoMigrate = &no
	strict, err := OpenPostgres(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	strict.Close()
	// Tamper only within an uncommitted transaction in the disposable database.
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE lm_schema_migrations SET checksum='unexpected' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if err = migratePlatform(context.Background(), tx, false); err == nil {
		t.Fatal("changed checksum accepted")
	}
	tx.Rollback()
	tx, err = s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM lm_schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if err = migratePlatform(context.Background(), tx, false); err == nil {
		t.Fatal("missing ledger accepted")
	}
	tx.Rollback()
}
