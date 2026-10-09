package monitor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/001_platform.sql
var platformSchema string
var ErrLeaseLost = errors.New("platform lease lost")

// The database clock controls lease expiry. Tokens fence observation writes and task completion.
type PostgresStore struct {
	DB           *sql.DB
	ctx          context.Context
	Scope, Owner string
	mu           sync.Mutex
	claims       map[string]string
}

func OpenPostgres(ctx context.Context, c Config) (*PostgresStore, error) {
	db, err := sql.Open("pgx", os.Getenv(c.Platform.Postgres.DSNEnv))
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL configuration invalid")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	s := &PostgresStore{DB: db, ctx: ctx, Scope: c.Platform.SubscriptionID, Owner: ID(), claims: map[string]string{}}
	cc, cancel := s.timeout()
	defer cancel()
	tx, err := db.BeginTx(cc, nil)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("PostgreSQL connection failed")
	}
	defer tx.Rollback()
	// Serialize additive migrations even when several replicas start together.
	if _, err = tx.ExecContext(cc, "SELECT pg_advisory_xact_lock(16160019029)"); err == nil {
		_, err = tx.ExecContext(cc, platformSchema)
	}
	if err == nil {
		_, err = tx.ExecContext(cc, "INSERT INTO lm_scopes(scope,room,binding) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", s.Scope, c.Subscription.RoomID, subscriptionBinding(c))
	}
	var room int64
	var binding string
	if err == nil {
		err = tx.QueryRowContext(cc, "SELECT room,binding FROM lm_scopes WHERE scope=$1", s.Scope).Scan(&room, &binding)
	}
	if err == nil && (room != c.Subscription.RoomID || binding != subscriptionBinding(c)) {
		err = fmt.Errorf("subscription scope is already bound to another room")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("PostgreSQL schema or subscription binding failed")
	}
	return s, nil
}
func (s *PostgresStore) timeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.ctx, 5*time.Second)
}
func (s *PostgresStore) Close() error {
	cc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.DB.ExecContext(cc, "UPDATE lm_scopes SET lease_until=clock_timestamp() WHERE scope=$1 AND owner=$2", s.Scope, s.Owner)
	return s.DB.Close()
}
func (s *PostgresStore) Leadership() (bool, error) {
	cc, cancel := s.timeout()
	defer cancel()
	var owner string
	err := s.DB.QueryRowContext(cc, `UPDATE lm_scopes SET owner=$2,lease_until=clock_timestamp()+interval '60 seconds'
 WHERE scope=$1 AND (owner=$2 OR lease_until<=clock_timestamp()) RETURNING owner`, s.Scope, s.Owner).Scan(&owner)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
func (s *PostgresStore) guard(ctx context.Context, tx *sql.Tx) error {
	var owner string
	err := tx.QueryRowContext(ctx, "SELECT owner FROM lm_scopes WHERE scope=$1 AND lease_until>clock_timestamp() FOR UPDATE", s.Scope).Scan(&owner)
	if err == sql.ErrNoRows || err == nil && owner != s.Owner {
		return ErrLeaseLost
	}
	return err
}
func (s *PostgresStore) bump(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "UPDATE lm_scopes SET version=version+1 WHERE scope=$1", s.Scope)
	return err
}
func (s *PostgresStore) Observe(o Observation, catchup bool, ttl int) (bool, error) {
	cc, cancel := s.timeout()
	defer cancel()
	tx, err := s.DB.BeginTx(cc, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = s.guard(cc, tx); err != nil {
		return false, err
	}
	var live int
	var start, key sql.NullString
	err = tx.QueryRowContext(cc, "SELECT live,start,key FROM lm_observations WHERE scope=$1 AND room=$2", s.Scope, o.RoomID).Scan(&live, &start, &key)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	missing := err == sql.ErrNoRows
	k, actual := "", o.Start
	if o.Live {
		if o.Start != "" {
			k = fmtRoom(o.RoomID) + ":start:" + o.Start
		} else if live == 1 {
			k = key.String
		} else {
			k = fmtRoom(o.RoomID) + ":local:" + ID()
		}
		if live == 1 && key.Valid && (o.Start == "" || !start.Valid || start.String == o.Start) {
			k = key.String
		}
		if actual == "" && live == 1 {
			actual = start.String
		}
	} else {
		actual = ""
	}
	l := 0
	if o.Live {
		l = 1
	}
	changed := missing || live != l || start.String != actual || key.String != k
	if changed {
		_, err = tx.ExecContext(cc, `INSERT INTO lm_observations(scope,room,live,start,key) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(scope,room) DO UPDATE SET live=excluded.live,start=excluded.start,key=excluded.key`, s.Scope, o.RoomID, l, nullable(actual), nullable(k))
		if err != nil {
			return false, err
		}
	}
	added := false
	if k != "" && !(o.Live && live == 1 && key.Valid && key.String == k) {
		o.Start = actual
		b, _ := json.Marshal(Notice{o, k, catchup})
		r, e := tx.ExecContext(cc, "INSERT INTO lm_jobs(scope,key,payload,next,expires) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", s.Scope, k, string(b), o.At, o.At+int64(ttl)*60000)
		if e != nil {
			return false, e
		}
		n, _ := r.RowsAffected()
		added = n > 0
	}
	if changed || added {
		if err = s.bump(cc, tx); err != nil {
			return false, err
		}
	}
	return added, tx.Commit()
}
func (s *PostgresStore) PollingPhase(room int64) (string, error) {
	cc, cancel := s.timeout()
	defer cancel()
	var live int
	var status sql.NullString
	err := s.DB.QueryRowContext(cc, "SELECT o.live,j.status FROM lm_observations o LEFT JOIN lm_jobs j ON o.scope=j.scope AND o.key=j.key WHERE o.scope=$1 AND o.room=$2", s.Scope, room).Scan(&live, &status)
	if err == sql.ErrNoRows {
		return "awaiting_start", nil
	}
	if err != nil {
		return "", err
	}
	if live != 1 {
		return "awaiting_start", nil
	}
	if status.String == "sent" {
		return "notified_live", nil
	}
	return "awaiting_notification", nil
}
func (s *PostgresStore) Due(now time.Time) (*Job, error)   { return s.claim(now, "") }
func (s *PostgresStore) ClaimKey(key string) (*Job, error) { return s.claim(time.Now(), key) }
func (s *PostgresStore) claim(now time.Time, key string) (*Job, error) {
	cc, cancel := s.timeout()
	defer cancel()
	tx, err := s.DB.BeginTx(cc, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.lockScope(cc, tx); err != nil {
		return nil, err
	}
	r, err := tx.ExecContext(cc, "UPDATE lm_jobs SET status='expired' WHERE scope=$1 AND status='pending' AND expires<=$2 AND lease_until<=clock_timestamp()", s.Scope, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	expired, _ := r.RowsAffected()
	token := ID()
	var j Job
	err = tx.QueryRowContext(cc, `WITH selected AS (SELECT key FROM lm_jobs WHERE scope=$1 AND status='pending' AND next<=$2 AND expires>$2
 AND lease_until<=clock_timestamp() AND ($4='' OR key=$4) ORDER BY next,key FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE lm_jobs j SET claim=$3,lease_until=clock_timestamp()+interval '60 seconds' FROM selected x
 WHERE j.scope=$1 AND j.key=x.key RETURNING j.key,j.payload,j.attempts,j.expires`, s.Scope, now.UnixMilli(), token, key).Scan(&j.Key, &j.Payload, &j.Attempts, &j.Expires)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	found := err == nil
	if expired > 0 || found {
		if err = s.bump(cc, tx); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	s.mu.Lock()
	s.claims[j.Key] = token
	s.mu.Unlock()
	return &j, nil
}
func (s *PostgresStore) Complete(key, status, code string, next int64, attempt bool) error {
	s.mu.Lock()
	token := s.claims[key]
	s.mu.Unlock()
	if token == "" {
		return ErrLeaseLost
	}
	cc, cancel := s.timeout()
	defer cancel()
	tx, err := s.DB.BeginTx(cc, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockScope(cc, tx); err != nil {
		return err
	}
	increment := 0
	if attempt {
		increment = 1
	}
	r, err := tx.ExecContext(cc, `UPDATE lm_jobs SET status=$4,last_error=$5,next=$6,attempts=attempts+$7,claim='',lease_until='-infinity'
 WHERE scope=$1 AND key=$2 AND claim=$3 AND status='pending' AND lease_until>clock_timestamp()`, s.Scope, key, token, status, nullable(code), next, increment)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrLeaseLost
	}
	if err = s.bump(cc, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.mu.Lock()
		delete(s.claims, key)
		s.mu.Unlock()
	}
	return err
}
func (s *PostgresStore) Sent(key string) error { return s.Complete(key, "sent", "", 0, false) }
func (s *PostgresStore) Failed(j *Job, e error, now time.Time) error {
	status := "failed"
	if Retryable(e) {
		status = "pending"
	}
	return s.Complete(j.Key, status, e.Error(), now.Add(RetryDelay(e, j.Attempts+1, 5)).UnixMilli(), true)
}
func (s *PostgresStore) Retry(now time.Time) (int64, error) {
	cc, cancel := s.timeout()
	defer cancel()
	tx, e := s.DB.BeginTx(cc, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if err := s.lockScope(cc, tx); err != nil {
		return 0, err
	}
	r, e := tx.ExecContext(cc, "UPDATE lm_jobs SET status='pending',next=$2,published=false WHERE scope=$1 AND status='failed' AND expires>$2", s.Scope, now.UnixMilli())
	if e != nil {
		return 0, e
	}
	n, _ := r.RowsAffected()
	if n > 0 {
		if e = s.bump(cc, tx); e != nil {
			return 0, e
		}
	}
	return n, tx.Commit()
}
func (s *PostgresStore) DataVersion() (int64, error) {
	cc, cancel := s.timeout()
	defer cancel()
	var v int64
	e := s.DB.QueryRowContext(cc, "SELECT version FROM lm_scopes WHERE scope=$1", s.Scope).Scan(&v)
	return v, e
}
func (s *PostgresStore) Counts() (map[string]int, error) {
	cc, cancel := s.timeout()
	defer cancel()
	rows, e := s.DB.QueryContext(cc, "SELECT status,count(*) FROM lm_jobs WHERE scope=$1 GROUP BY status", s.Scope)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if e = rows.Scan(&k, &n); e != nil {
			return nil, e
		}
		m[k] = n
	}
	return m, rows.Err()
}
func (s *PostgresStore) NextWake() (time.Time, error) {
	cc, cancel := s.timeout()
	defer cancel()
	var at sql.NullInt64
	e := s.DB.QueryRowContext(cc, `SELECT MIN(GREATEST(LEAST(next,expires),COALESCE(CASE WHEN lease_until>clock_timestamp() THEN (extract(epoch from lease_until)*1000)::bigint END,0))) FROM lm_jobs WHERE scope=$1 AND status='pending'`, s.Scope).Scan(&at)
	if e != nil || !at.Valid {
		return time.Time{}, e
	}
	return time.UnixMilli(at.Int64), nil
}
func (s *PostgresStore) NextCleanupAt(days int, now time.Time) (time.Time, error) {
	if days == 0 {
		return time.Time{}, nil
	}
	cc, cancel := s.timeout()
	defer cancel()
	var last int64
	e := s.DB.QueryRowContext(cc, "SELECT last_cleanup FROM lm_scopes WHERE scope=$1", s.Scope).Scan(&last)
	if last == 0 {
		return now, e
	}
	return Earliest(now.Add(24*time.Hour), time.UnixMilli(last).Add(24*time.Hour)), e
}
func (s *PostgresStore) CleanupHistory(days int, now time.Time) (int64, error) {
	if days == 0 {
		return 0, nil
	}
	cc, cancel := s.timeout()
	defer cancel()
	tx, e := s.DB.BeginTx(cc, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var last int64
	if e = tx.QueryRowContext(cc, "SELECT last_cleanup FROM lm_scopes WHERE scope=$1 FOR UPDATE", s.Scope).Scan(&last); e != nil {
		return 0, e
	}
	if last > 0 && now.UnixMilli() >= last && now.Sub(time.UnixMilli(last)) < 24*time.Hour {
		return 0, nil
	}
	r, e := tx.ExecContext(cc, `DELETE FROM lm_jobs WHERE scope=$1 AND key IN (SELECT key FROM lm_jobs WHERE scope=$1 AND status IN ('sent','failed','expired') AND expires<$2 AND key NOT IN (SELECT key FROM lm_observations WHERE scope=$1 AND key IS NOT NULL) ORDER BY expires,key LIMIT 200)`, s.Scope, now.Add(-time.Duration(days)*24*time.Hour).UnixMilli())
	if e != nil {
		return 0, e
	}
	n, _ := r.RowsAffected()
	if _, e = tx.ExecContext(cc, "UPDATE lm_scopes SET last_cleanup=$2,version=version+1 WHERE scope=$1", s.Scope, now.UnixMilli()); e != nil {
		return 0, e
	}
	return n, tx.Commit()
}

// Outbox rows and jobs are one transaction: a Redis outage never loses the durable task.
func (s *PostgresStore) Outbox() (map[string]string, error) {
	cc, cancel := s.timeout()
	defer cancel()
	rows, e := s.DB.QueryContext(cc, "SELECT key,payload FROM lm_jobs WHERE scope=$1 AND status='pending' AND NOT published ORDER BY next LIMIT 10", s.Scope)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, p string
		if e = rows.Scan(&k, &p); e != nil {
			return nil, e
		}
		m[k] = p
	}
	return m, rows.Err()
}
func (s *PostgresStore) Published(key string) error {
	cc, cancel := s.timeout()
	defer cancel()
	_, e := s.DB.ExecContext(cc, "UPDATE lm_jobs SET published=true WHERE scope=$1 AND key=$2", s.Scope, key)
	return e
}

func subscriptionBinding(c Config) string {
	var target []string
	if c.Notification.Mode == "feishu_group" {
		target = []string{c.Notification.Mode, os.Getenv(c.Notification.Group.Webhook)}
	} else {
		p := c.Notification.Private
		target = []string{c.Notification.Mode, os.Getenv(p.AppID), p.IDType, os.Getenv(p.ID)}
	}
	b, _ := json.Marshal(target)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *PostgresStore) lockScope(ctx context.Context, tx *sql.Tx) error {
	var scope string
	return tx.QueryRowContext(ctx, "SELECT scope FROM lm_scopes WHERE scope=$1 FOR UPDATE", s.Scope).Scan(&scope)
}
