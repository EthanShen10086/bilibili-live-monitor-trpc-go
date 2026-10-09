package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

// Notifications owns durable deliveries, not broker acknowledgements or control-plane mutations.
type Notifications struct{ DB *DB }

func (n Notifications) Route(ctx context.Context, event domain.Event, subs []domain.Subscription, snapshot bool) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Type != "live.started.v1" {
		return nil
	}
	return transaction(ctx, n.DB.SQL, func(tx *sql.Tx) error {
		if !snapshot {
			r, err := tx.ExecContext(ctx, "INSERT INTO ep_consumed(consumer,event_id) VALUES('notifications',$1) ON CONFLICT DO NOTHING", event.ID)
			if err != nil {
				return err
			}
			count, err := r.RowsAffected()
			if err != nil {
				return err
			}
			if count == 0 {
				return nil
			}
		}
		for _, s := range subs {
			if !s.Enabled || s.RoomID != event.Data.RequestedRoom || (!snapshot && s.CreatedAt.After(event.Time)) {
				continue
			}
			policy := s.Policy.MonitorConfig(s.RoomID)
			if !policy.InWindow(event.Time) || !policy.InWindow(time.Now()) {
				continue
			}
			expires := event.Time.Add(time.Duration(s.Policy.TTLMinutes) * time.Minute)
			if !expires.After(time.Now()) {
				continue
			}
			text := monitor.FormatNotice(monitor.Notice{Observation: event.Data.Observation, Key: event.Data.SessionKey, Catchup: event.Data.Catchup}, s.RoomID)
			for _, target := range s.TargetIDs {
				_, err := tx.ExecContext(ctx, "INSERT INTO ep_jobs(id,tenant_id,subscription_id,target_id,event_id,state,text,next,expires) SELECT $1,$2,$3,$4,$5,'pending',$6,clock_timestamp(),$7 WHERE EXISTS(SELECT 1 FROM ep_subscription_targets st JOIN ep_subscriptions sub ON sub.tenant_id=st.tenant_id AND sub.id=st.id WHERE st.tenant_id=$2 AND st.id=$3 AND st.target_id=$4 AND sub.enabled) ON CONFLICT DO NOTHING", monitor.ID(), s.TenantID, s.ID, target, event.ID, text, expires)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (n Notifications) Claim(ctx context.Context, owner string) (*domain.Job, error) {
	var job domain.Job
	err := transaction(ctx, n.DB.SQL, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "UPDATE ep_jobs SET state='expired',owner='' WHERE state IN ('pending','sending') AND expires<=clock_timestamp() AND (lease_until IS NULL OR lease_until<clock_timestamp())"); e != nil {
			return e
		}
		e := tx.QueryRowContext(ctx, "SELECT j.id,j.tenant_id,j.subscription_id,j.target_id,j.event_id,j.text,j.attempts,j.expires,j.next FROM ep_jobs j JOIN ep_subscriptions s ON s.tenant_id=j.tenant_id AND s.id=j.subscription_id JOIN ep_subscription_targets st ON st.tenant_id=j.tenant_id AND st.id=j.subscription_id AND st.target_id=j.target_id WHERE s.enabled AND j.expires>clock_timestamp() AND ((j.state='pending' AND j.next<=clock_timestamp()) OR (j.state='sending' AND j.lease_until<clock_timestamp())) ORDER BY j.next FOR UPDATE OF j SKIP LOCKED LIMIT 1").Scan(&job.ID, &job.TenantID, &job.SubscriptionID, &job.TargetID, &job.EventID, &job.Text, &job.Attempts, &job.Expires, &job.Next)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		job.LeaseOwner = owner
		job.State = "sending"
		return affected(tx.ExecContext(ctx, "UPDATE ep_jobs SET state='sending',owner=$2,lease_until=clock_timestamp()+interval '60 seconds',attempts=attempts+1 WHERE id=$1", job.ID, owner))
	})
	if err != nil {
		return nil, err
	}
	if job.ID == "" {
		return nil, nil
	}
	job.Attempts++
	return &job, nil
}

func (c Control) ResolveTarget(ctx context.Context, tenant, id string) (domain.Target, error) {
	var t domain.Target
	var encrypted string
	t.TenantID = tenant
	t.ID = id
	e := c.DB.SQL.QueryRowContext(ctx, "SELECT name,kind,version,credentials FROM ep_targets WHERE tenant_id=$1 AND id=$2", tenant, id).Scan(&t.Name, &t.Kind, &t.Version, &encrypted)
	if e != nil {
		return t, classify(e)
	}
	t.Credentials, e = c.DB.Vault.Open(tenant+":"+id, encrypted)
	return t, e
}

func (n Notifications) Finish(ctx context.Context, job domain.Job, sendErr error) error {
	state := "sent"
	next := time.Now()
	if sendErr != nil {
		state = "pending"
		next = next.Add(monitor.RetryDelay(sendErr, job.Attempts, 10))
		if !monitor.Retryable(sendErr) || job.Attempts >= 12 {
			state = "failed"
		}
	}
	return affected(n.DB.SQL.ExecContext(ctx, "UPDATE ep_jobs SET state=$3,next=$4,owner='',lease_until=NULL,sent_at=CASE WHEN $3='sent' THEN clock_timestamp() ELSE NULL END WHERE id=$1 AND owner=$2 AND state='sending' AND lease_until>clock_timestamp()", job.ID, job.LeaseOwner, state, next))
}

func (n Notifications) AllSent(ctx context.Context, s domain.Subscription, eventID string) (bool, error) {
	var count int
	e := n.DB.SQL.QueryRowContext(ctx, "SELECT count(*) FROM ep_jobs WHERE tenant_id=$1 AND subscription_id=$2 AND event_id=$3 AND state='sent' AND target_id=ANY($4)", s.TenantID, s.ID, eventID, s.TargetIDs).Scan(&count)
	return count == len(s.TargetIDs) && count > 0, e
}

func (n Notifications) Jobs(ctx context.Context, p domain.Principal, tenant string) ([]domain.Job, error) {
	if e := authorize(ctx, n.DB.SQL, p, tenant, "read"); e != nil {
		return nil, e
	}
	rows, e := n.DB.SQL.QueryContext(ctx, "SELECT id,subscription_id,target_id,event_id,state,attempts,expires,next,COALESCE((extract(epoch FROM sent_at-created_at)*1000)::bigint,0) FROM ep_jobs WHERE tenant_id=$1 ORDER BY created_at DESC,id LIMIT 1000", tenant)
	if e != nil {
		return nil, e
	}
	defer resource.Close(rows)
	out := []domain.Job{}
	for rows.Next() {
		j := domain.Job{TenantID: tenant}
		if e = rows.Scan(&j.ID, &j.SubscriptionID, &j.TargetID, &j.EventID, &j.State, &j.Attempts, &j.Expires, &j.Next, &j.LatencyMillis); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (n Notifications) Retry(ctx context.Context, p domain.Principal, tenant, id string) error {
	return transaction(ctx, n.DB.SQL, func(tx *sql.Tx) error {
		if e := authorize(ctx, tx, p, tenant, "operate"); e != nil {
			return e
		}
		e := affected(tx.ExecContext(ctx, "UPDATE ep_jobs SET state='pending',next=clock_timestamp(),attempts=0 WHERE tenant_id=$1 AND id=$2 AND state='failed' AND expires>clock_timestamp()", tenant, id))
		if e != nil {
			return e
		}
		return audit(ctx, tx, p, tenant, "notification.retry", id)
	})
}

func (n Notifications) Counts(ctx context.Context) (map[string]int64, error) {
	rows, e := n.DB.SQL.QueryContext(ctx, "SELECT state,count(*) FROM ep_jobs GROUP BY state")
	if e != nil {
		return nil, e
	}
	defer resource.Close(rows)
	out := map[string]int64{}
	for rows.Next() {
		var state string
		var count int64
		if e = rows.Scan(&state, &count); e != nil {
			return nil, e
		}
		out[state] = count
	}
	return out, rows.Err()
}
