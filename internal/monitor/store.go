package monitor

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }
type Job struct {
	Key, Payload string
	Attempts     int
	Expires      int64
}

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func OpenStore(file string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", file)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=DELETE; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS observations(room INTEGER PRIMARY KEY,live INTEGER NOT NULL,start TEXT,key TEXT);
CREATE TABLE IF NOT EXISTS maintenance(id INTEGER PRIMARY KEY,last_cleanup INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS jobs(key TEXT PRIMARY KEY,payload TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next INTEGER NOT NULL,expires INTEGER NOT NULL,last_error TEXT);
    CREATE INDEX IF NOT EXISTS jobs_pending_next ON jobs(next) WHERE status='pending';
    CREATE INDEX IF NOT EXISTS jobs_pending_expires ON jobs(expires) WHERE status='pending';`)
	if e != nil {
		db.Close()
		return nil, e
	}
	if e = os.Chmod(file, 0600); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Observe(o Observation, catchup bool, ttl int) (bool, error) {
	tx, e := s.DB.Begin()
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var live int
	var start, key sql.NullString
	e = tx.QueryRow("SELECT live,start,key FROM observations WHERE room=?", o.RoomID).Scan(&live, &start, &key)
	if e != nil && e != sql.ErrNoRows {
		return false, e
	}
	k := ""
	actual := o.Start
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
	if e == sql.ErrNoRows || live != l || start.String != actual || key.String != k {
		_, e = tx.Exec("INSERT OR REPLACE INTO observations(room,live,start,key) VALUES(?,?,?,?)", o.RoomID, l, nullable(actual), nullable(k))
		if e != nil {
			return false, e
		}
	}
	if o.Live && live == 1 && key.Valid && key.String == k {
		return false, tx.Commit()
	}
	added := false
	if k != "" {
		o.Start = actual
		n := Notice{o, k, catchup}
		b, _ := json.Marshal(n)
		r, err := tx.Exec("INSERT OR IGNORE INTO jobs(key,payload,next,expires) VALUES(?,?,?,?)", k, string(b), o.At, o.At+int64(ttl)*60000)
		if err != nil {
			return false, err
		}
		count, _ := r.RowsAffected()
		added = count > 0
	}
	return added, tx.Commit()
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (s *Store) Due(now time.Time) (*Job, error) {
	_, e := s.DB.Exec("UPDATE jobs SET status='expired' WHERE status='pending' AND expires<=?", now.UnixMilli())
	if e != nil {
		return nil, e
	}
	var j Job
	e = s.DB.QueryRow("SELECT key,payload,attempts,expires FROM jobs WHERE status='pending' AND next<=? ORDER BY next LIMIT 1", now.UnixMilli()).Scan(&j.Key, &j.Payload, &j.Attempts, &j.Expires)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	return &j, e
}
func (s *Store) Sent(k string) error {
	_, e := s.DB.Exec("UPDATE jobs SET status='sent',last_error=NULL WHERE key=?", k)
	return e
}
func (s *Store) Failed(j *Job, err error, now time.Time) error {
	state := "failed"
	if Retryable(err) {
		state = "pending"
	}
	_, e := s.DB.Exec("UPDATE jobs SET status=?,attempts=attempts+1,next=?,last_error=? WHERE key=?", state, now.Add(RetryDelay(err, j.Attempts+1, 5)).UnixMilli(), err.Error(), j.Key)
	return e
}
func (s *Store) Retry(now time.Time) (int64, error) {
	r, e := s.DB.Exec("UPDATE jobs SET status='pending',next=?,last_error=NULL WHERE status='failed' AND expires>?", now.UnixMilli(), now.UnixMilli())
	if e != nil {
		return 0, e
	}
	return r.RowsAffected()
}
func (s *Store) Counts() (map[string]int, error) {
	rows, e := s.DB.Query("SELECT status,count(*) FROM jobs GROUP BY status")
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

func (s *Store) DataVersion() (int64, error) {
	var v int64
	e := s.DB.QueryRow("PRAGMA data_version").Scan(&v)
	return v, e
}
func (s *Store) NextWake() (time.Time, error) {
	var next, expires sql.NullInt64
	if e := s.DB.QueryRow("SELECT MIN(next),MIN(expires) FROM jobs WHERE status='pending'").Scan(&next, &expires); e != nil {
		return time.Time{}, e
	}
	if !next.Valid {
		return time.Time{}, nil
	}
	return time.UnixMilli(min(next.Int64, expires.Int64)), nil
}

func (s *Store) PollingPhase(room int64) (string, error) {
	var live int
	var status sql.NullString
	e := s.DB.QueryRow("SELECT o.live,j.status FROM observations o LEFT JOIN jobs j ON o.key=j.key WHERE o.room=?", room).Scan(&live, &status)
	if e == sql.ErrNoRows {
		return "awaiting_start", nil
	}
	if e != nil {
		return "", e
	}
	if live != 1 {
		return "awaiting_start", nil
	}
	if status.String == "sent" {
		return "notified_live", nil
	}
	return "awaiting_notification", nil
}

func (s *Store) NextCleanupAt(days int, now time.Time) (time.Time, error) {
	if days == 0 {
		return time.Time{}, nil
	}
	var last int64
	e := s.DB.QueryRow("SELECT last_cleanup FROM maintenance WHERE id=1").Scan(&last)
	if e == sql.ErrNoRows {
		return now, nil
	}
	if e != nil {
		return time.Time{}, e
	}
	return Earliest(now.Add(24*time.Hour), time.UnixMilli(last).Add(24*time.Hour)), nil
}
func (s *Store) CleanupHistory(days int, now time.Time) (int64, error) {
	if days == 0 {
		return 0, nil
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var last int64
	e = tx.QueryRow("SELECT last_cleanup FROM maintenance WHERE id=1").Scan(&last)
	if e != nil && e != sql.ErrNoRows {
		return 0, e
	}
	if e == nil && now.UnixMilli() >= last && now.Sub(time.UnixMilli(last)) < 24*time.Hour {
		return 0, nil
	}
	r, e := tx.Exec(`DELETE FROM jobs WHERE key IN (SELECT key FROM jobs
 WHERE status IN ('sent','failed','expired') AND expires<?
 AND key NOT IN (SELECT key FROM observations WHERE key IS NOT NULL)
 ORDER BY expires,key LIMIT 200)`, now.Add(-time.Duration(days)*24*time.Hour).UnixMilli())
	if e != nil {
		return 0, e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return 0, e
	}
	if _, e = tx.Exec("INSERT OR REPLACE INTO maintenance(id,last_cleanup) VALUES(1,?)", now.UnixMilli()); e != nil {
		return 0, e
	}
	return n, tx.Commit()
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) OldestPending() (int64, error) {
	var oldest sql.NullInt64
	err := s.DB.QueryRow("SELECT MIN(CAST(json_extract(payload,'$.detectedAt') AS INTEGER)) FROM jobs WHERE status='pending'").Scan(&oldest)
	return oldest.Int64, err
}
