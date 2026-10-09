package store

import (
	"context"
	"database/sql"
	"time"
)

// Cleanup retains active-session dedup keys, unfinished work and unpublished events.
// A long-running stream therefore cannot be resent merely because retention elapsed.
func (e Events) Cleanup(ctx context.Context, days int) error {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	return transaction(ctx, e.DB.SQL, func(tx *sql.Tx) error {
		queries := []string{
			`DELETE FROM ep_jobs j WHERE j.state IN ('sent','failed','expired','disabled') AND j.expires<$1 AND NOT EXISTS (SELECT 1 FROM ep_events ev JOIN ep_rooms r ON r.room=ev.room AND r.session_key=ev.session_key WHERE ev.id=j.event_id AND r.observation->>'live'='true')`,
			`DELETE FROM ep_projection_events p WHERE p.at<$1 AND NOT EXISTS(SELECT 1 FROM ep_rooms r WHERE r.room=p.room AND r.session_key=p.session_key AND r.observation->>'live'='true')`,
			`DELETE FROM ep_events ev WHERE ev.published AND ev.at<$1 AND NOT EXISTS(SELECT 1 FROM ep_jobs j WHERE j.event_id=ev.id) AND NOT EXISTS(SELECT 1 FROM ep_rooms r WHERE r.room=ev.room AND r.session_key=ev.session_key AND r.observation->>'live'='true')`,
			`DELETE FROM ep_consumed c WHERE c.at<$1 AND NOT EXISTS(SELECT 1 FROM ep_events ev WHERE ev.id=c.event_id)`,
			`DELETE FROM ep_deadletters WHERE at<$1`,
			`DELETE FROM ep_replays WHERE created_at<$1 AND state<>'pending'`,
			`DELETE FROM ep_audit WHERE at<$1`,
		}
		for _, q := range queries {
			if _, err := tx.ExecContext(ctx, q, cutoff); err != nil {
				return err
			}
		}
		return nil
	})
}
