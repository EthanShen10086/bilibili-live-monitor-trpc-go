//go:build eventintegration

package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/secrets"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("EVENT_TEST_POSTGRES")
	if dsn == "" {
		t.Fatal("EVENT_TEST_POSTGRES must identify disposable PostgreSQL")
	}
	ctx := context.Background()
	vault, e := secrets.New("test", map[string]string{"test": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))})
	if e != nil {
		t.Fatal(e)
	}
	admin, e := Open(ctx, dsn, vault)
	if e != nil {
		t.Fatal(e)
	}
	schema := "ep_test_" + monitor.ID()
	if _, e = admin.SQL.ExecContext(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := Open(ctx, u.String(), vault)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); admin.SQL.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	return db
}

func fixture(t *testing.T, db *DB) (domain.Principal, domain.Tenant, domain.Target, domain.Subscription) {
	t.Helper()
	ctx := context.Background()
	c := Control{db}
	p := domain.Principal{Subject: "owner", PlatformAdmin: true}
	tenant, e := c.CreateTenant(ctx, p, domain.Tenant{Name: "test", MaxSubscriptions: 2, MaxTargets: 2})
	if e != nil {
		t.Fatal(e)
	}
	target, e := c.SaveTarget(ctx, p, tenant.ID, domain.Target{Name: "mail", Kind: "smtp", Credentials: domain.Credentials{SMTPHost: "localhost", SMTPPort: 2525, From: "a@example.com", Recipient: "b@example.com"}}, true)
	if e != nil {
		t.Fatal(e)
	}
	policy := domain.DefaultPolicy()
	policy.Weekdays = []int{1, 2, 3, 4, 5, 6, 7}
	policy.Start = "00:00"
	policy.End = "24:00"
	sub, e := c.SaveSubscription(ctx, p, tenant.ID, domain.Subscription{RoomID: 1616, Enabled: true, Policy: policy, TargetIDs: []string{target.ID}}, true)
	if e != nil {
		t.Fatal(e)
	}
	return p, tenant, target, sub
}

func TestTenantIsolationQuotaVersionAndCredentialRotation(t *testing.T) {
	db := testDB(t)
	p, tenant, target, sub := fixture(t, db)
	ctx := context.Background()
	c := Control{db}
	outsider := domain.Principal{Subject: "other"}
	if _, e := c.Targets(ctx, outsider, tenant.ID); !errors.Is(e, domain.ErrForbidden) {
		t.Fatal("cross tenant access", e)
	}
	second, e := c.CreateTenant(ctx, p, domain.Tenant{Name: "other"})
	if e != nil {
		t.Fatal(e)
	}
	sub.TargetIDs = []string{target.ID}
	if _, e = c.SaveSubscription(ctx, p, second.ID, sub, true); !errors.Is(e, domain.ErrInvalid) {
		t.Fatal("cross tenant target binding", e)
	}
	sub.TargetIDs = []string{target.ID}
	sub.Enabled = false
	if _, e = c.SaveSubscription(ctx, p, tenant.ID, sub, false); e != nil {
		t.Fatal(e)
	}
	if _, e = c.SaveSubscription(ctx, p, tenant.ID, sub, false); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("stale version", e)
	}
	if e = c.PutMember(ctx, p, tenant.ID, domain.Member{Subject: p.Subject, Role: "viewer"}, false); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("last owner removed", e)
	}
	if e = c.RotateCredentials(ctx, p, tenant.ID); e != nil {
		t.Fatal(e)
	}
	resolved, e := c.ResolveTarget(ctx, tenant.ID, target.ID)
	if e != nil || resolved.Credentials.Recipient != "b@example.com" {
		t.Fatal("rotation corrupted target", e)
	}
	var encrypted string
	if e = db.SQL.QueryRowContext(ctx, "SELECT credentials FROM ep_targets WHERE tenant_id=$1 AND id=$2", tenant.ID, target.ID).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(encrypted, "b@example.com") {
		t.Fatal("plaintext credential")
	}
}

func TestRoomFencingOutboxDuplicateRoutingAndReplay(t *testing.T) {
	db := testDB(t)
	p, tenant, _, sub := fixture(t, db)
	ctx := context.Background()
	events := Events{db}
	control := Control{db}
	notifications := Notifications{db}
	projections := Projections{db}
	acquired, e := events.Acquire(ctx, 1616, "first")
	if e != nil || !acquired {
		t.Fatal(e)
	}
	if acquired, e = events.Acquire(ctx, 1616, "second"); e != nil || acquired {
		t.Fatal("lease duplicated", e)
	}
	o := monitor.Observation{RoomID: 11163068, Live: true, Start: "2026-10-09T00:00:00Z", Title: "test", At: time.Now().UnixMilli()}
	if e = events.Observe(ctx, 1616, "stale", o, false, time.Second); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("stale owner wrote", e)
	}
	if e = events.Observe(ctx, 1616, "first", o, false, time.Second); e != nil {
		t.Fatal(e)
	}
	var event domain.Event
	calls := 0
	publish := func(_ context.Context, e domain.Event) error { event = e; calls++; return nil }
	if e = events.PublishBatch(ctx, publish); e != nil {
		t.Fatal(e)
	}
	if e = events.PublishBatch(ctx, publish); e != nil || calls != 1 {
		t.Fatal("outbox repeated acknowledged event", e)
	}
	subs, e := control.RoomSubscriptions(ctx, 1616)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = notifications.Route(ctx, event, subs, false); e != nil {
			t.Fatal(e)
		}
	}
	jobs, e := notifications.Jobs(ctx, p, tenant.ID)
	if e != nil || len(jobs) != 1 {
		t.Fatalf("routing duplicate: %v %v", jobs, e)
	}
	job, e := notifications.Claim(ctx, "sender-a")
	if e != nil || job == nil {
		t.Fatal(e)
	}
	if another, e := notifications.Claim(ctx, "sender-b"); e != nil || another != nil {
		t.Fatal("double claim", e)
	}
	wrong := *job
	wrong.LeaseOwner = "wrong"
	if e = notifications.Finish(ctx, wrong, nil); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("lease fencing failed", e)
	}
	if e = notifications.Finish(ctx, *job, nil); e != nil {
		t.Fatal(e)
	}
	if sent, e := notifications.AllSent(ctx, sub, event.ID); e != nil || !sent {
		t.Fatal("sent state missing", e)
	}
	replay, e := projections.RequestReplay(ctx, p, tenant.ID, time.Now().Add(-time.Minute), time.Now(), 90)
	if e != nil {
		t.Fatal(e)
	}
	if e = projections.RebuildNext(ctx); e != nil {
		t.Fatal(e)
	}
	runs, e := projections.Replays(ctx, p, tenant.ID)
	if e != nil || len(runs) != 1 || runs[0].ID != replay.ID || runs[0].Processed != 1 {
		t.Fatal("replay failed", e)
	}
	jobs, e = notifications.Jobs(ctx, p, tenant.ID)
	if e != nil || len(jobs) != 1 || jobs[0].State != "sent" {
		t.Fatal("replay changed deliveries", e)
	}
	stats, e := projections.Statistics(ctx, p, tenant.ID)
	if e != nil || stats.Sessions != 1 {
		t.Fatal(fmt.Sprint(stats), e)
	}
}

func TestImportAtomicDisabledAndIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	policy := domain.DefaultPolicy()
	now := time.Now()
	notice := monitor.Notice{Observation: monitor.Observation{RoomID: 1616, Live: true, Start: "2026-10-09T00:00:00.000Z", At: now.UnixMilli()}, Key: "legacy-session", Catchup: true}
	input := LegacySnapshot{SourceID: "snapshot-1", Owner: "legacy-owner", Subscription: domain.Subscription{RoomID: 1616, Policy: policy, TargetIDs: []string{"legacy-target"}}, Target: domain.Target{Name: "legacy", Kind: "smtp", Credentials: domain.Credentials{SMTPHost: "localhost", SMTPPort: 2525, From: "a@example.com", Recipient: "b@example.com"}}, Observation: notice.Observation, SessionKey: notice.Key, Jobs: []LegacyJob{{Notice: notice, State: "sent", Next: now, Expires: now.Add(time.Minute)}}}
	report, e := db.ImportLegacy(ctx, input)
	if e != nil || report.AlreadyImported {
		t.Fatalf("import %v %v", report, e)
	}
	report, e = db.ImportLegacy(ctx, input)
	if e != nil || !report.AlreadyImported {
		t.Fatalf("repeat %v %v", report, e)
	}
	var enabled bool
	var session, state string
	if e = db.SQL.QueryRow("SELECT enabled FROM ep_subscriptions").Scan(&enabled); e != nil || enabled {
		t.Fatal("import enabled sending", e)
	}
	if e = db.SQL.QueryRow("SELECT session_key FROM ep_rooms").Scan(&session); e != nil || session != notice.Key {
		t.Fatal("lost dedup session", e)
	}
	if e = db.SQL.QueryRow("SELECT state FROM ep_jobs").Scan(&state); e != nil || state != "sent" {
		t.Fatal("lost sent state", e)
	}
	if n, e := (Events{db}).OutboxCount(ctx); e != nil || n != 0 {
		t.Fatal("import queued broker publication", e)
	}
	if job, e := (Notifications{db}).Claim(ctx, "sender"); e != nil || job != nil {
		t.Fatal("import sends notification", e)
	}
	input.SourceID = "changed-snapshot"
	if _, e = db.ImportLegacy(ctx, input); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("replaced existing snapshot", e)
	}
}

func TestRetentionPreservesActiveSessionAndUnpublished(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	_, _, _, sub := fixture(t, db)
	events := Events{db}
	now := time.Now().Add(-100 * 24 * time.Hour)
	acquired, e := events.Acquire(ctx, sub.RoomID, "detector")
	if e != nil || !acquired {
		t.Fatal(e)
	}
	if e = events.Observe(ctx, sub.RoomID, "detector", monitor.Observation{RoomID: sub.RoomID, Live: true, Start: "old-session", At: now.UnixMilli()}, false, time.Minute); e != nil {
		t.Fatal(e)
	}
	if e = events.PublishBatch(ctx, func(context.Context, domain.Event) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if e = events.Cleanup(ctx, 90); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = db.SQL.QueryRow("SELECT count(*) FROM ep_events").Scan(&count); e != nil || count != 1 {
		t.Fatal("active session was pruned", e)
	}
	if _, e = db.SQL.Exec("UPDATE ep_rooms SET observation='{}'"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE ep_events SET published=false"); e != nil {
		t.Fatal(e)
	}
	if e = events.Cleanup(ctx, 90); e != nil {
		t.Fatal(e)
	}
	if count, e := events.OutboxCount(ctx); e != nil || count != 1 {
		t.Fatal("unpublished event was pruned", e)
	}
}
