package monitor

import (
	"context"
	"testing"
	"time"
)

func TestPlatformConfigIsolation(t *testing.T) {
	c := testConfig(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Platform.Storage = "redis"
	if c.Validate() == nil {
		t.Fatal("invalid storage accepted")
	}
	c.Platform.Storage = "sqlite"
	c.Platform.Queue = "redis_streams"
	if c.Validate() == nil {
		t.Fatal("distributed queue with SQLite")
	}
	c.Platform.Storage = "postgres"
	c.Platform.Postgres.DSNEnv = "TEST_PG"
	c.Platform.Redis.URLEnv = "TEST_REDIS"
	c.Platform.SubscriptionID = "room-1616"
	c.Deployment.Active = "cloud"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Platform.SubscriptionID = ""
	if c.Validate() == nil {
		t.Fatal("missing scope")
	}
	c.Platform.SubscriptionID = "room-1616"
	c.Detector.Mode = "official"
	if c.Validate() == nil {
		t.Fatal("distributed official mode requires separate lifecycle")
	}
	c.Detector.Mode = "polling"
	c.Platform.Cache = "kafka"
	if c.Validate() == nil {
		t.Fatal("unsupported cache")
	}
	c.Platform.Cache = "redis"
	t.Setenv("FEISHU_WEBHOOK", "https://open.feishu.cn/open-apis/bot/v2/hook/fake")
	t.Setenv("FEISHU_WEBHOOK_SECRET", "fake")
	if c.Credentials() == nil {
		t.Fatal("selected backend credentials missing")
	}
	t.Setenv("TEST_PG", "postgres://fake")
	t.Setenv("TEST_REDIS", "redis://fake")
	if err := c.Credentials(); err != nil {
		t.Fatal(err)
	}
	if err := DeploySwitch(context.Background(), t.TempDir(), c, "local"); err == nil {
		t.Fatal("shared DB state must not be copied as SQLite")
	}
}

func TestMemoryCacheCopiesAndExpires(t *testing.T) {
	c := NewMemoryCache()
	ctx := context.Background()
	b := []byte("safe")
	c.Put(ctx, "k", b, 10*time.Millisecond)
	b[0] = 'x'
	v, ok := c.Get(ctx, "k")
	if !ok || string(v) != "safe" {
		t.Fatal(v, ok)
	}
	v[0] = 'x'
	v, _ = c.Get(ctx, "k")
	if string(v) != "safe" {
		t.Fatal("shared mutable cache")
	}
	time.Sleep(15 * time.Millisecond)
	if _, ok = c.Get(ctx, "k"); ok {
		t.Fatal("cache did not expire")
	}
}
