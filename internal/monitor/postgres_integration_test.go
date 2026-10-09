package monitor

import (
	"context"
	"errors"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func platformConfig(t *testing.T) Config {
	t.Helper()
	dsn := os.Getenv("MONITOR_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set MONITOR_TEST_POSTGRES to disposable PostgreSQL")
	}
	c := testConfig(t)
	c.Deployment.Active = "cloud"
	c.Platform.Storage = "postgres"
	c.Platform.SubscriptionID = "test-" + ID()
	c.Platform.Postgres.DSNEnv = "TEST_PLATFORM_PG"
	t.Setenv("TEST_PLATFORM_PG", dsn)
	return c
}
func platformStore(t *testing.T, c Config) *PostgresStore {
	t.Helper()
	s, e := OpenPostgres(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func clearScope(t *testing.T, s *PostgresStore) {
	t.Helper()
	t.Cleanup(func() {
		s.DB.Exec("DELETE FROM lm_jobs WHERE scope=$1", s.Scope)
		s.DB.Exec("DELETE FROM lm_observations WHERE scope=$1", s.Scope)
		s.DB.Exec("DELETE FROM lm_scopes WHERE scope=$1", s.Scope)
	})
}
func TestPostgresLeaseDedupeAndConcurrentClaims(t *testing.T) {
	c := platformConfig(t)
	a := platformStore(t, c)
	clearScope(t, a)
	b := platformStore(t, c)
	if ok, e := a.Leadership(); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if ok, e := b.Leadership(); e != nil || ok {
		t.Fatal("second detector acquired lease", ok, e)
	}
	o := Observation{RoomID: 1, Live: true, Start: "2026-10-09T10:00:00Z", At: time.Now().UnixMilli(), Title: "test"}
	if _, e := b.Observe(o, false, 30); !errors.Is(e, ErrLeaseLost) {
		t.Fatal("unfenced observation", e)
	}
	if added, e := a.Observe(o, false, 30); e != nil || !added {
		t.Fatal(added, e)
	}
	if added, e := a.Observe(o, false, 30); e != nil || added {
		t.Fatal("duplicate", added, e)
	}
	var count atomic.Int32
	var wg sync.WaitGroup
	for _, s := range []*PostgresStore{a, b} {
		wg.Add(1)
		go func(s *PostgresStore) {
			defer wg.Done()
			j, e := s.Due(time.Now())
			if e != nil {
				t.Error(e)
				return
			}
			if j != nil {
				count.Add(1)
				if e = s.Sent(j.Key); e != nil {
					t.Error(e)
				}
			}
		}(s)
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("double claim", count.Load())
	}
	if phase, e := a.PollingPhase(1); e != nil || phase != "notified_live" {
		t.Fatal(phase, e)
	}
	reopened := platformStore(t, c)
	if phase, e := reopened.PollingPhase(1); e != nil || phase != "notified_live" {
		t.Fatal("restart dedupe", phase, e)
	}
	if _, e := a.DB.Exec("UPDATE lm_scopes SET lease_until=clock_timestamp()-interval '1 second' WHERE scope=$1", a.Scope); e != nil {
		t.Fatal(e)
	}
	if ok, e := b.Leadership(); e != nil || !ok {
		t.Fatal("failover", ok, e)
	}
	if _, e := a.Observe(o, false, 30); !errors.Is(e, ErrLeaseLost) {
		t.Fatal("old leader wrote", e)
	}
	c.Subscription.RoomID++
	if s, e := OpenPostgres(context.Background(), c); e == nil {
		s.Close()
		t.Fatal("scope changed room")
	}
}
func TestPostgresLeaseExpiryRetryAndCleanup(t *testing.T) {
	c := platformConfig(t)
	a := platformStore(t, c)
	clearScope(t, a)
	b := platformStore(t, c)
	a.Leadership()
	now := time.Now()
	o := Observation{RoomID: 2, Live: true, Start: "session", At: now.UnixMilli()}
	if _, e := a.Observe(o, true, 30); e != nil {
		t.Fatal(e)
	}
	j, e := a.Due(now.Add(time.Millisecond))
	if e != nil || j == nil {
		t.Fatal(j, e)
	}
	if _, e = a.DB.Exec("UPDATE lm_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE scope=$1 AND key=$2", a.Scope, j.Key); e != nil {
		t.Fatal(e)
	}
	if e = a.Sent(j.Key); !errors.Is(e, ErrLeaseLost) {
		t.Fatal("stale completion", e)
	}
	recovered, e := b.Due(time.Now())
	if e != nil || recovered == nil {
		t.Fatal("claim recovery", recovered, e)
	}
	if e = b.Failed(recovered, &RemoteError{"test", "permanent", false}, time.Now()); e != nil {
		t.Fatal(e)
	}
	if n, e := a.Retry(time.Now()); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	recovered, e = a.Due(time.Now())
	if e != nil || recovered == nil {
		t.Fatal(recovered, e)
	}
	if e = a.Sent(recovered.Key); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec("UPDATE lm_jobs SET expires=0 WHERE scope=$1", a.Scope); e != nil {
		t.Fatal(e)
	}
	if n, e := a.CleanupHistory(90, time.Now()); e != nil || n != 0 {
		t.Fatal("current dedupe deleted", n, e)
	}
	if _, e = a.DB.Exec("INSERT INTO lm_jobs(scope,key,payload,status,next,expires) VALUES($1,'old','{}','sent',0,0),($1,'pending','{}','pending',0,0)", a.Scope); e != nil {
		t.Fatal(e)
	}
	if n, e := a.CleanupHistory(90, time.Now().Add(25*time.Hour)); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	counts, e := a.Counts()
	if e != nil || counts["pending"] != 1 || counts["sent"] != 1 {
		t.Fatal(counts, e)
	}
}
func TestRedisOutboxAndCacheIntegration(t *testing.T) {
	c := platformConfig(t)
	url := os.Getenv("MONITOR_TEST_REDIS")
	if url == "" {
		t.Skip("set MONITOR_TEST_REDIS")
	}
	c.Platform.Redis.URLEnv = "TEST_PLATFORM_REDIS"
	c.Platform.Cache = "redis"
	t.Setenv("TEST_PLATFORM_REDIS", url)
	a := platformStore(t, c)
	clearScope(t, a)
	a.Leadership()
	o := Observation{RoomID: 1, Live: true, Start: "redis-session", At: time.Now().UnixMilli()}
	a.Observe(o, false, 30)
	q, e := OpenStreamQueue(c, a)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	defer q.Client.Del(context.Background(), q.Stream)
	if e = q.Publish(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = q.Publish(context.Background()); e != nil {
		t.Fatal(e)
	}
	job, id, e := q.Receive(context.Background())
	if e != nil || job == nil || id == "" {
		t.Fatal(job, id, e)
	}
	if e = a.Sent(job.Key); e != nil {
		t.Fatal(e)
	}
	if e = q.Ack(context.Background(), id); e != nil {
		t.Fatal(e)
	}
	if j, _, e := q.Receive(context.Background()); e != nil || j != nil {
		t.Fatal("duplicate send", j, e)
	}
	cache, e := OpenCache(c)
	if e != nil {
		t.Fatal(e)
	}
	defer cache.Close()
	cache.Put(context.Background(), "test", []byte("status"), 30*time.Millisecond)
	if b, ok := cache.Get(context.Background(), "test"); !ok || string(b) != "status" {
		t.Fatal(string(b), ok)
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := cache.Get(context.Background(), "test"); ok {
		t.Fatal("redis ttl")
	}
	// A chosen Redis connection failure fails startup, never silently changes configured mode.
	// Simulate a broken Redis transport after startup; the new session remains claimable from the DB.
	a.Observe(Observation{RoomID: 1, Live: true, Start: "redis-next-session", At: time.Now().UnixMilli()}, false, 30)
	q.Client.Close()
	if e = q.Publish(context.Background()); e == nil {
		t.Fatal("Redis outage not surfaced")
	}
	if job, e := a.Due(time.Now()); e != nil || job == nil {
		t.Fatal("DB recovery lost task", job, e)
	}
	t.Setenv("TEST_PLATFORM_REDIS", "redis://127.0.0.1:1")
	if bad, e := OpenStreamQueue(c, a); e == nil {
		bad.Close()
		t.Fatal("missing Redis accepted")
	}
}

func TestPostgresSenderRuntimeUsesDurableClaim(t *testing.T) {
	c := platformConfig(t)
	c.Platform.Role = "sender"
	t.Setenv(c.Notification.Group.Webhook, "https://open.feishu.cn/open-apis/bot/v2/hook/fake")
	t.Setenv(c.Notification.Group.Secret, "fake")
	a := platformStore(t, c)
	clearScope(t, a)
	if ok, e := a.Leadership(); e != nil || !ok {
		t.Fatal(ok, e)
	}
	o := Observation{RoomID: 11163068, Live: true, Start: "runtime-session", At: time.Now().UnixMilli()}
	if _, e := a.Observe(o, false, 30); e != nil {
		t.Fatal(e)
	}
	var sends atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &HTTP{Client: &http.Client{Transport: platformRoundTrip(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Host, "feishu") {
			t.Error("sender tried Bilibili")
		}
		sends.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0}`)), Header: http.Header{}}, nil
	})}}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		root := t.TempDir()
		go func() { done <- Run(ctx, root, c, h) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		phase, e := a.PollingPhase(o.RoomID)
		if e == nil && phase == "notified_live" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if phase, e := a.PollingPhase(o.RoomID); e != nil || phase != "notified_live" {
		t.Fatal(phase, e)
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case e := <-done:
			if e != nil && ctx.Err() == nil {
				t.Fatal(e)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("sender shutdown timeout")
		}
	}
	if sends.Load() != 1 {
		t.Fatal("multiple senders duplicated send", sends.Load())
	}
}

type platformRoundTrip func(*http.Request) (*http.Response, error)

func (f platformRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPostgresImportsSQLiteDedupeAtomically(t *testing.T) {
	c := platformConfig(t)
	a := platformStore(t, c)
	clearScope(t, a)
	root := t.TempDir()
	b, _ := yaml.Marshal(c)
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), b, 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := OpenStore(filepath.Join(root, "var/state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	o := Observation{RoomID: 11163068, Live: true, Start: "import-session", At: time.Now().UnixMilli()}
	legacy.Observe(o, true, 30)
	job, _ := legacy.Due(time.Now())
	legacy.Sent(job.Key)
	legacy.Close()
	if n, e := a.ImportSQLite(context.Background(), root, c); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if phase, e := a.PollingPhase(o.RoomID); e != nil || phase != "notified_live" {
		t.Fatal("import lost dedupe", phase, e)
	}
	if _, e := a.ImportSQLite(context.Background(), root, c); e == nil {
		t.Fatal("nonempty target overwritten")
	}
	original, e := OpenStore(filepath.Join(root, "var/state.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer original.Close()
	if phase, e := original.PollingPhase(o.RoomID); e != nil || phase != "notified_live" {
		t.Fatal("source changed", phase, e)
	}
}
