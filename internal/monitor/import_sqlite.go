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

	"gopkg.in/yaml.v3"
)

// ImportSQLite is an explicit, atomic one-way migration into an empty subscription.
// It never changes the original SQLite file or silently merges two live systems.
func (s *PostgresStore) ImportSQLite(ctx context.Context, source string, c Config) (int, error) {
	if err := AssertStopped(source); err != nil {
		return 0, err
	}
	release, err := Lock(source, "instance")
	if err != nil {
		return 0, err
	}
	defer release()
	b, err := os.ReadFile(filepath.Join(source, "config.yaml"))
	if err != nil {
		return 0, err
	}
	var original Config
	if err = yaml.Unmarshal(b, &original); err != nil || original.Subscription.RoomID != c.Subscription.RoomID {
		return 0, fmt.Errorf("source room differs from target subscription")
	}
	file := filepath.Join(source, "var/state.sqlite")
	if _, err = os.Stat(file); err != nil {
		return 0, err
	}
	u := url.URL{Scheme: "file", Path: file}
	sourceDB, err := sql.Open("sqlite", u.String()+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer resource.Close(sourceDB)
	sourceDB.SetMaxOpenConns(1)
	cc, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(cc, nil)
	if err != nil {
		return 0, err
	}
	defer resource.Rollback(tx)
	if err = s.lockScope(cc, tx); err != nil {
		return 0, err
	}
	var empty bool
	err = tx.QueryRowContext(cc, `SELECT lease_until<=clock_timestamp() AND NOT EXISTS(SELECT 1 FROM lm_jobs WHERE scope=$1) AND NOT EXISTS(SELECT 1 FROM lm_observations WHERE scope=$1) FROM lm_scopes WHERE scope=$1`, s.Scope).Scan(&empty)
	if err != nil {
		return 0, err
	}
	if !empty {
		return 0, fmt.Errorf("target subscription must be empty and detector lease inactive")
	}
	rows, err := sourceDB.QueryContext(cc, "SELECT room,live,start,key FROM observations")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var room int64
		var live int
		var start, key sql.NullString
		if err = rows.Scan(&room, &live, &start, &key); err != nil {
			resource.Close(rows)
			return 0, err
		}
		if _, err = tx.ExecContext(cc, "INSERT INTO lm_observations(scope,room,live,start,key) VALUES($1,$2,$3,$4,$5)", s.Scope, room, live, start, key); err != nil {
			resource.Close(rows)
			return 0, err
		}
	}
	err = rows.Err()
	resource.Close(rows)
	if err != nil {
		return 0, err
	}
	rows, err = sourceDB.QueryContext(cc, "SELECT key,payload,status,attempts,next,expires,last_error FROM jobs")
	if err != nil {
		return 0, err
	}
	defer resource.Close(rows)
	count := 0
	for rows.Next() {
		var key, payload, status string
		var attempts int
		var next, expires int64
		var last sql.NullString
		if err = rows.Scan(&key, &payload, &status, &attempts, &next, &expires, &last); err != nil {
			return 0, err
		}
		count++
		if count > 10000 {
			return 0, fmt.Errorf("import exceeds 10000 jobs; use a reviewed bulk migration")
		}
		switch status {
		case "pending", "sent", "failed", "expired":
		default:
			return 0, fmt.Errorf("invalid source job status")
		}
		if len(payload) > 2*1024*1024 {
			return 0, fmt.Errorf("source payload exceeds limit")
		}
		if _, err = tx.ExecContext(cc, "INSERT INTO lm_jobs(scope,key,payload,status,attempts,next,expires,last_error) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", s.Scope, key, payload, status, attempts, next, expires, last); err != nil {
			return 0, err
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if err = s.bump(cc, tx); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}
