package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
)

type LegacyJob struct {
	Notice        monitor.Notice
	State         string
	Attempts      int
	Next, Expires time.Time
}
type LegacySnapshot struct {
	SourceID, Owner string
	Subscription    domain.Subscription
	Target          domain.Target
	Observation     monitor.Observation
	SessionKey      string
	Jobs            []LegacyJob
}
type ImportReport struct {
	SourceID        string `json:"source_id"`
	Jobs            int    `json:"jobs"`
	AlreadyImported bool   `json:"already_imported"`
}

// ImportLegacy creates an isolated, disabled default subscription atomically.
// It neither publishes events nor calls notification providers.
func (d *DB) ImportLegacy(ctx context.Context, in LegacySnapshot) (ImportReport, error) {
	report := ImportReport{SourceID: in.SourceID, Jobs: len(in.Jobs)}
	if in.SourceID == "" || in.Owner == "" || in.Subscription.Validate() != nil || in.Target.Validate() != nil {
		return report, domain.ErrInvalid
	}
	tenant, sub, target := "legacy-default", "legacy-room", "legacy-target"
	sealed, err := d.Vault.Seal(tenant+":"+target, in.Target.Credentials)
	if err != nil {
		return report, err
	}
	err = transaction(ctx, d.SQL, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(16160019033)"); e != nil {
			return e
		}
		var existing string
		e := tx.QueryRowContext(ctx, "SELECT source_id FROM ep_imports WHERE tenant_id=$1 AND subscription_id=$2", tenant, sub).Scan(&existing)
		if e == nil {
			if existing != in.SourceID {
				return domain.ErrConflict
			}
			report.AlreadyImported = true
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO ep_tenants VALUES($1,'Legacy default',100,100)", tenant); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO ep_members VALUES($1,$2,'owner')", tenant, in.Owner); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO ep_targets(tenant_id,id,name,kind,credentials) VALUES($1,$2,$3,$4,$5)", tenant, target, in.Target.Name, in.Target.Kind, sealed); e != nil {
			return e
		}
		policy, e := json.Marshal(in.Subscription.Policy)
		if e != nil {
			return e
		}
		createdAt := time.Now()
		for _, job := range in.Jobs {
			if at := time.UnixMilli(job.Notice.At); at.Before(createdAt) {
				createdAt = at
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO ep_subscriptions(tenant_id,id,room,enabled,policy,created_at) VALUES($1,$2,$3,false,$4,$5)", tenant, sub, in.Subscription.RoomID, policy, createdAt); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO ep_subscription_targets VALUES($1,$2,$3)", tenant, sub, target); e != nil {
			return e
		}
		observation, e := json.Marshal(in.Observation)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO ep_rooms(room,observation,session_key) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", in.Subscription.RoomID, observation, in.SessionKey); e != nil {
			return e
		}
		for _, job := range in.Jobs {
			if job.Notice.Key == "" || job.Notice.RoomID != in.Observation.RoomID || job.Attempts < 0 {
				return domain.ErrInvalid
			}
			switch job.State {
			case "pending", "sent", "failed", "expired":
			default:
				return domain.ErrInvalid
			}
			event := domain.Event{SpecVersion: "1.0", ID: EventID(in.Subscription.RoomID, job.Notice.Key, "live.started.v1"), Source: "/bilibili/rooms", Type: "live.started.v1", Time: time.UnixMilli(job.Notice.At).UTC(), DataContentType: "application/json", Data: domain.EventData{SchemaVersion: 1, RequestedRoom: in.Subscription.RoomID, SessionKey: job.Notice.Key, Catchup: job.Notice.Catchup, Observation: job.Notice.Observation}}
			raw, e := json.Marshal(event)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO ep_events(id,room,session_key,type,at,envelope,published) VALUES($1,$2,$3,$4,$5,$6,true) ON CONFLICT DO NOTHING", event.ID, event.Data.RequestedRoom, event.Data.SessionKey, event.Type, event.Time, raw); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO ep_consumed(consumer,event_id) VALUES('notifications',$1) ON CONFLICT DO NOTHING", event.ID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO ep_jobs(id,tenant_id,subscription_id,target_id,event_id,state,text,attempts,next,expires,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", monitor.ID(), tenant, sub, target, event.ID, job.State, monitor.FormatNotice(job.Notice, in.Subscription.RoomID), job.Attempts, job.Next, job.Expires, event.Time); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO ep_imports(source_id,tenant_id,subscription_id) VALUES($1,$2,$3)", in.SourceID, tenant, sub); e != nil {
			return e
		}
		return audit(ctx, tx, domain.Principal{Subject: in.Owner}, tenant, "legacy.import", sub)
	})
	return report, err
}
