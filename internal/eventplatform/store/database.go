// Package store implements module-owned PostgreSQL repositories. No legacy tables are altered.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/secrets"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

//go:embed schema.sql
var schema string

//go:embed schema_v2.sql
var schemaV2 string

var migrations = []string{schema, schemaV2}

type DB struct {
	SQL   *sql.DB
	Vault *secrets.Vault
}

func Open(ctx context.Context, dsn string, v *secrets.Vault) (*DB, error) {
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if e = db.PingContext(ctx); e != nil {
		resource.Close(db)
		return nil, errors.New("platform database unavailable")
	}
	return &DB{db, v}, nil
}
func (d *DB) Close() error { return d.SQL.Close() }
func transaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer resource.Rollback(tx)
	if e = fn(tx); e != nil {
		return classify(e)
	}
	return classify(tx.Commit())
}

func classify(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return domain.ErrConflict
		case "23503", "23514", "22P02":
			return domain.ErrInvalid
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func (d *DB) Migrate(ctx context.Context) error {
	return transaction(ctx, d.SQL, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(16160019032)"); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS ep_migrations(version integer PRIMARY KEY,checksum text NOT NULL)"); e != nil {
			return e
		}
		var maxVersion int
		if e := tx.QueryRowContext(ctx, "SELECT COALESCE(max(version),0) FROM ep_migrations").Scan(&maxVersion); e != nil {
			return e
		}
		if maxVersion > len(migrations) {
			return errors.New("unsupported event platform schema")
		}
		for i, sqlText := range migrations {
			version := i + 1
			checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(sqlText)))
			var sum string
			err := tx.QueryRowContext(ctx, "SELECT checksum FROM ep_migrations WHERE version=$1", version).Scan(&sum)
			if err == nil {
				if sum != checksum {
					return errors.New("changed applied event platform migration")
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err = tx.ExecContext(ctx, sqlText); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO ep_migrations VALUES($1,$2)", version, checksum); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) CheckSchema(ctx context.Context) error {
	var count int
	if e := d.SQL.QueryRowContext(ctx, "SELECT count(*) FROM ep_migrations").Scan(&count); e != nil || count != len(migrations) {
		return errors.New("run event-platform migrate first")
	}
	for i, sqlText := range migrations {
		var sum string
		e := d.SQL.QueryRowContext(ctx, "SELECT checksum FROM ep_migrations WHERE version=$1", i+1).Scan(&sum)
		if e != nil || sum != fmt.Sprintf("%x", sha256.Sum256([]byte(sqlText))) {
			return errors.New("run event-platform migrate first")
		}
	}
	return nil
}

func affected(result sql.Result, err error) error {
	if err != nil {
		return classify(err)
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return domain.ErrConflict
	}
	return nil
}
