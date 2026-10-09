package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/propagation"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

// Events owns observations, fenced room leases and the immutable transactional outbox.
type Events struct{ DB *DB }

func EventID(room int64, session, kind string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", room, session, kind))))
}

func (e Events) Rooms(ctx context.Context) ([]int64, error) {
	rows, err := e.DB.SQL.QueryContext(ctx, "SELECT DISTINCT room FROM ep_subscriptions WHERE enabled ORDER BY room LIMIT 10000")
	if err != nil {
		return nil, err
	}
	defer resource.Close(rows)
	out := []int64{}
	for rows.Next() {
		var room int64
		if err = rows.Scan(&room); err != nil {
			return nil, err
		}
		out = append(out, room)
	}
	return out, rows.Err()
}

func (e Events) Acquire(ctx context.Context, room int64, owner string) (bool, error) {
	if _, err := e.DB.SQL.ExecContext(ctx, "INSERT INTO ep_rooms(room) VALUES($1) ON CONFLICT DO NOTHING", room); err != nil {
		return false, err
	}
	r, err := e.DB.SQL.ExecContext(ctx, "UPDATE ep_rooms SET owner=$2,lease_until=clock_timestamp()+interval '20 seconds' WHERE room=$1 AND next_probe<=clock_timestamp() AND (lease_until IS NULL OR lease_until<clock_timestamp())", room, owner)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (e Events) Delay(ctx context.Context, room int64, owner string, delay time.Duration) error {
	return affected(e.DB.SQL.ExecContext(ctx, "UPDATE ep_rooms SET next_probe=clock_timestamp()+$3*interval '1 millisecond',lease_until=NULL WHERE room=$1 AND owner=$2 AND lease_until>clock_timestamp()", room, owner, delay.Milliseconds()))
}

func (e Events) Observe(ctx context.Context, room int64, owner string, o monitor.Observation, catchup bool, delay time.Duration) error {
	return transaction(ctx, e.DB.SQL, func(tx *sql.Tx) error {
		var raw []byte
		var session string
		if err := tx.QueryRowContext(ctx, "SELECT observation,session_key FROM ep_rooms WHERE room=$1 AND owner=$2 AND lease_until>clock_timestamp() FOR UPDATE", room, owner).Scan(&raw, &session); err != nil {
			return err
		}
		var prev monitor.Observation
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &prev); err != nil {
				return err
			}
		}
		kind := ""
		if o.Live {
			if !prev.Live || (o.Start != "" && prev.Start != "" && o.Start != prev.Start) {
				if o.Start != "" {
					session = fmt.Sprintf("%d:start:%s", o.RoomID, o.Start)
				} else {
					session = fmt.Sprintf("%d:local:%s", o.RoomID, monitor.ID())
				}
				kind = "live.started.v1"
			} else if session == "" {
				return errors.New("missing live session")
			}
		} else if prev.Live {
			kind = "live.ended.v1"
		}
		if kind != "" {
			event := domain.Event{SpecVersion: "1.0", ID: EventID(room, session, kind), Source: "/bilibili/rooms", Type: kind, Subject: fmt.Sprint(room), Time: time.UnixMilli(o.At).UTC(), DataContentType: "application/json", Data: domain.EventData{SchemaVersion: 1, RequestedRoom: room, SessionKey: session, Catchup: catchup || prev.At == 0, Observation: o}}
			carrier := propagation.MapCarrier{}
			propagation.TraceContext{}.Inject(ctx, carrier)
			event.TraceParent = carrier.Get("traceparent")
			event.TraceState = carrier.Get("tracestate")
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO ep_events(id,room,session_key,type,at,envelope) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", event.ID, room, session, kind, event.Time, payload); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(o)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE ep_rooms SET observation=$3,session_key=$4,next_probe=clock_timestamp()+$5*interval '1 millisecond',lease_until=NULL WHERE room=$1 AND owner=$2", room, owner, payload, session, delay.Milliseconds())
		return err
	})
}

// PublishBatch holds row locks through broker acknowledgement. A crash may duplicate, never silently drop an event.
func (e Events) PublishBatch(ctx context.Context, publish func(context.Context, domain.Event) error) error {
	return transaction(ctx, e.DB.SQL, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT envelope FROM ep_events WHERE NOT published ORDER BY at,id LIMIT 20 FOR UPDATE SKIP LOCKED")
		if err != nil {
			return err
		}
		events := []domain.Event{}
		for rows.Next() {
			var raw []byte
			var event domain.Event
			if err = rows.Scan(&raw); err != nil {
				resource.Close(rows)
				return err
			}
			if err = json.Unmarshal(raw, &event); err != nil {
				resource.Close(rows)
				return err
			}
			events = append(events, event)
		}
		err = rows.Err()
		resource.Close(rows)
		if err != nil {
			return err
		}
		for _, event := range events {
			if err = publish(ctx, event); err != nil {
				return err
			}
			if err = affected(tx.ExecContext(ctx, "UPDATE ep_events SET published=true WHERE id=$1", event.ID)); err != nil {
				return err
			}
		}
		return nil
	})
}

// LiveSnapshots reconcile late subscribers/window entry without inventing a new live session.
func (e Events) LiveSnapshots(ctx context.Context) ([]domain.Event, error) {
	rows, err := e.DB.SQL.QueryContext(ctx, "SELECT room,session_key,observation FROM ep_rooms WHERE observation->>'live'='true' ORDER BY room LIMIT 10000")
	if err != nil {
		return nil, err
	}
	defer resource.Close(rows)
	out := []domain.Event{}
	for rows.Next() {
		var room int64
		var session string
		var raw []byte
		var o monitor.Observation
		if err = rows.Scan(&room, &session, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		out = append(out, domain.Event{SpecVersion: "1.0", ID: EventID(room, session, "live.started.v1"), Source: "/bilibili/rooms", Type: "live.started.v1", Subject: fmt.Sprint(room), Time: time.UnixMilli(o.At).UTC(), DataContentType: "application/json", Data: domain.EventData{SchemaVersion: 1, RequestedRoom: room, SessionKey: session, Catchup: true, Observation: o}})
	}
	return out, rows.Err()
}

func (e Events) Deadletter(ctx context.Context, consumer, id, reason string) error {
	_, err := e.DB.SQL.ExecContext(ctx, "INSERT INTO ep_deadletters(consumer,event_id,reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", consumer, id, reason)
	return err
}

func (e Events) Project(ctx context.Context, event domain.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(event.Data.Observation)
	if err != nil {
		return err
	}
	_, err = e.DB.SQL.ExecContext(ctx, "INSERT INTO ep_projection_events VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", event.Data.RequestedRoom, event.ID, event.Data.SessionKey, event.Type, event.Time, raw)
	return err
}

func (e Events) OutboxCount(ctx context.Context) (int64, error) {
	var n int64
	err := e.DB.SQL.QueryRowContext(ctx, "SELECT count(*) FROM ep_events WHERE NOT published").Scan(&n)
	return n, err
}

// DueRooms prioritizes overdue rooms, so one failed or slow room cannot starve the rest.
func (e Events) DueRooms(ctx context.Context) ([]int64, error) {
	rows, err := e.DB.SQL.QueryContext(ctx, `SELECT s.room FROM (SELECT DISTINCT room FROM ep_subscriptions WHERE enabled) s LEFT JOIN ep_rooms r ON r.room=s.room WHERE r.room IS NULL OR (r.next_probe<=clock_timestamp() AND (r.lease_until IS NULL OR r.lease_until<clock_timestamp())) ORDER BY r.next_probe NULLS FIRST,s.room LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer resource.Close(rows)
	out := []int64{}
	for rows.Next() {
		var room int64
		if err = rows.Scan(&room); err != nil {
			return nil, err
		}
		out = append(out, room)
	}
	return out, rows.Err()
}

// Routed gates late-subscriber reconciliation on actual broker publication/consumption.
// Snapshots cannot silently bypass the event transport during a broker outage.
func (e Events) Routed(ctx context.Context, id string) (bool, error) {
	var routed bool
	err := e.DB.SQL.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ep_events ev JOIN ep_consumed c ON c.event_id=ev.id AND c.consumer='notifications' WHERE ev.id=$1 AND ev.published)`, id).Scan(&routed)
	return routed, err
}
