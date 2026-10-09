package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

// Projections owns rebuildable statistics and tenant-scoped replay requests.
type (
	Projections struct{ DB *DB }
	Statistics  struct {
		Sessions             int64            `json:"sessions"`
		KnownDurationSeconds *float64         `json:"known_duration_seconds"`
		Notifications        map[string]int64 `json:"notifications"`
		MeanLatencyMillis    *float64         `json:"mean_latency_ms"`
	}
)

type Replay struct {
	ID        string    `json:"id"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	State     string    `json:"state"`
	Processed int64     `json:"processed"`
}

func (p Projections) Statistics(ctx context.Context, principal domain.Principal, tenant string) (Statistics, error) {
	s := Statistics{Notifications: map[string]int64{}}
	if e := authorize(ctx, p.DB.SQL, principal, tenant, "read"); e != nil {
		return s, e
	}
	e := p.DB.SQL.QueryRowContext(ctx, `WITH visible AS (SELECT DISTINCT pe.* FROM ep_projection_events pe JOIN ep_subscriptions sub ON sub.room=pe.room AND sub.tenant_id=$1 AND pe.at>=sub.created_at), starts AS (SELECT * FROM visible WHERE type='live.started.v1') SELECT count(*),(SELECT sum(extract(epoch FROM ending.at-starting.at)) FROM starts starting JOIN visible ending ON ending.room=starting.room AND ending.session_key=starting.session_key AND ending.type='live.ended.v1' WHERE ending.at>=starting.at) FROM starts`, tenant).Scan(&s.Sessions, &s.KnownDurationSeconds)
	if e != nil {
		return s, e
	}
	rows, e := p.DB.SQL.QueryContext(ctx, "SELECT state,count(*) FROM ep_jobs WHERE tenant_id=$1 GROUP BY state", tenant)
	if e != nil {
		return s, e
	}
	for rows.Next() {
		var state string
		var count int64
		if e = rows.Scan(&state, &count); e != nil {
			resource.Close(rows)
			return s, e
		}
		s.Notifications[state] = count
	}
	e = rows.Err()
	resource.Close(rows)
	if e != nil {
		return s, e
	}
	e = p.DB.SQL.QueryRowContext(ctx, "SELECT avg(extract(epoch FROM sent_at-created_at)*1000) FROM ep_jobs WHERE tenant_id=$1 AND state='sent'", tenant).Scan(&s.MeanLatencyMillis)
	return s, e
}

func (p Projections) RequestReplay(ctx context.Context, principal domain.Principal, tenant string, from, to time.Time, retentionDays int) (Replay, error) {
	r := Replay{ID: monitor.ID(), From: from, To: to, State: "pending"}
	if from.IsZero() || !to.After(from) || to.After(time.Now().Add(time.Second)) || from.Before(time.Now().Add(-time.Duration(retentionDays)*24*time.Hour)) {
		return r, domain.ErrInvalid
	}
	e := transaction(ctx, p.DB.SQL, func(tx *sql.Tx) error {
		if e := authorize(ctx, tx, principal, tenant, "operate"); e != nil {
			return e
		}
		if _, e := lockTenant(ctx, tx, tenant); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM ep_replays WHERE tenant_id=$1 AND state='pending'", tenant).Scan(&count); e != nil {
			return e
		}
		if count >= 3 {
			return domain.ErrQuota
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO ep_replays(id,tenant_id,from_at,to_at,state) VALUES($1,$2,$3,$4,'pending')", r.ID, tenant, from, to); e != nil {
			return e
		}
		return audit(ctx, tx, principal, tenant, "statistics.replay", r.ID)
	})
	return r, e
}

func (p Projections) Replays(ctx context.Context, principal domain.Principal, tenant string) ([]Replay, error) {
	if e := authorize(ctx, p.DB.SQL, principal, tenant, "read"); e != nil {
		return nil, e
	}
	rows, e := p.DB.SQL.QueryContext(ctx, "SELECT id,from_at,to_at,state,processed FROM ep_replays WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 100", tenant)
	if e != nil {
		return nil, e
	}
	defer resource.Close(rows)
	out := []Replay{}
	for rows.Next() {
		var r Replay
		if e = rows.Scan(&r.ID, &r.From, &r.To, &r.State, &r.Processed); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p Projections) RebuildNext(ctx context.Context) error {
	return transaction(ctx, p.DB.SQL, func(tx *sql.Tx) error {
		var id, tenant string
		var from, to time.Time
		e := tx.QueryRowContext(ctx, "SELECT id,tenant_id,from_at,to_at FROM ep_replays WHERE state='pending' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&id, &tenant, &from, &to)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT DISTINCT ev.envelope FROM ep_events ev JOIN ep_subscriptions sub ON sub.room=ev.room AND sub.tenant_id=$1 WHERE ev.at BETWEEN $2 AND $3 AND ev.at>=sub.created_at ORDER BY ev.envelope LIMIT 10001", tenant, from, to)
		if e != nil {
			return e
		}
		events := []domain.Event{}
		for rows.Next() {
			var raw []byte
			var event domain.Event
			if e = rows.Scan(&raw); e != nil {
				resource.Close(rows)
				return e
			}
			if e = json.Unmarshal(raw, &event); e != nil {
				resource.Close(rows)
				return e
			}
			events = append(events, event)
		}
		e = rows.Err()
		resource.Close(rows)
		if e != nil {
			return e
		}
		if len(events) > 10000 {
			_, e = tx.ExecContext(ctx, "UPDATE ep_replays SET state='range_too_large' WHERE id=$1", id)
			return e
		}
		for _, event := range events {
			raw, e := json.Marshal(event.Data.Observation)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO ep_projection_events VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", event.Data.RequestedRoom, event.ID, event.Data.SessionKey, event.Type, event.Time, raw); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, "UPDATE ep_replays SET state='completed',processed=$2 WHERE id=$1", id, len(events))
		return e
	})
}
