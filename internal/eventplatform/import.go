package eventplatform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/store"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

// Import reads a stopped legacy SQLite snapshot in immutable read-only mode.
func Import(ctx context.Context, c Config, root, file, owner string, stopped bool) (store.ImportReport, error) {
	var report store.ImportReport
	if !stopped || owner == "" {
		return report, errors.New("explicit stopped-sender declaration and owner subject required")
	}
	legacy, e := monitor.Load(root)
	if e != nil {
		return report, e
	}
	if legacy.Detector.Mode != "polling" || legacy.Platform.StorageMode() != "sqlite" {
		return report, errors.New("import supports legacy SQLite polling only")
	}
	bytes, e := os.ReadFile(file)
	if e != nil {
		return report, e
	}
	hash := sha256.Sum256(bytes)
	abs, e := filepath.Abs(file)
	if e != nil {
		return report, e
	}
	source, e := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&immutable=1"}).String())
	if e != nil {
		return report, e
	}
	defer resource.Close(source)
	source.SetMaxOpenConns(1)
	snapshot := store.LegacySnapshot{SourceID: fmt.Sprintf("sqlite:%x", hash), Owner: owner}
	snapshot.Subscription = domain.Subscription{RoomID: legacy.Subscription.RoomID, TargetIDs: []string{"legacy-target"}, Policy: domain.Policy{Timezone: legacy.Schedule.Timezone, Weekdays: legacy.Schedule.Weekdays, Start: legacy.Schedule.Start, End: legacy.Schedule.End, IntervalSeconds: legacy.PollingSeconds(), NotifiedSeconds: legacy.NotifiedLiveSeconds(), TTLMinutes: legacy.Notification.TTL}}
	credentials := domain.Credentials{}
	if legacy.Notification.Mode == "feishu_group" {
		credentials.Webhook = os.Getenv(legacy.Notification.Group.Webhook)
		credentials.Secret = os.Getenv(legacy.Notification.Group.Secret)
	} else {
		credentials.AppID = os.Getenv(legacy.Notification.Private.AppID)
		credentials.Secret = os.Getenv(legacy.Notification.Private.Secret)
		credentials.Recipient = os.Getenv(legacy.Notification.Private.ID)
		credentials.IDType = legacy.Notification.Private.IDType
	}
	snapshot.Target = domain.Target{Name: "Imported legacy channel", Kind: legacy.Notification.Mode, Credentials: credentials}
	var live int
	var start, key sql.NullString
	e = source.QueryRowContext(ctx, "SELECT room,live,start,key FROM observations LIMIT 1").Scan(&snapshot.Observation.RoomID, &live, &start, &key)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return report, e
	}
	snapshot.Observation.Live = live == 1
	snapshot.Observation.Start = start.String
	snapshot.Observation.At = time.Now().UnixMilli()
	snapshot.SessionKey = key.String
	rows, e := source.QueryContext(ctx, "SELECT payload,status,attempts,next,expires FROM jobs ORDER BY next")
	if e != nil {
		return report, e
	}
	for rows.Next() {
		var job store.LegacyJob
		var payload string
		var next, expires int64
		if e = rows.Scan(&payload, &job.State, &job.Attempts, &next, &expires); e != nil {
			resource.Close(rows)
			return report, e
		}
		if e = json.Unmarshal([]byte(payload), &job.Notice); e != nil {
			resource.Close(rows)
			return report, e
		}
		job.Next = time.UnixMilli(next)
		job.Expires = time.UnixMilli(expires)
		snapshot.Jobs = append(snapshot.Jobs, job)
	}
	e = rows.Err()
	resource.Close(rows)
	if e != nil {
		return report, e
	}
	db, e := store.Open(ctx, c.DSN, c.Vault)
	if e != nil {
		return report, e
	}
	defer resource.Close(db)
	if e = db.CheckSchema(ctx); e != nil {
		return report, e
	}
	return db.ImportLegacy(ctx, snapshot)
}
