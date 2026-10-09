package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestDetectorCacheFreshnessAndFallback(t *testing.T) {
	c := testConfig(t)
	now := time.Now()
	cache := NewMemoryCache()
	calls := 0
	d := cachedDetector{cache: cache, now: func() time.Time { return now }, Detector: testDetector(func(context.Context, Config) (Observation, error) {
		calls++
		return Observation{RoomID: c.Subscription.RoomID, Live: true, At: now.UnixMilli()}, nil
	})}
	ctx := context.Background()
	for range 2 {
		if _, err := d.Probe(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("fresh observation did not avoid repeated upstream probe", calls)
	}
	key := "room-observation-v1:" + strconv.FormatInt(c.Subscription.RoomID, 10)
	for _, invalid := range []Observation{
		{RoomID: c.Subscription.RoomID, At: now.Add(-5 * time.Second).UnixMilli()},
		{RoomID: c.Subscription.RoomID, At: now.Add(time.Second).UnixMilli()},
		{RoomID: c.Subscription.RoomID + 1, At: now.UnixMilli()},
	} {
		raw, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		cache.Put(ctx, key, raw, time.Minute)
		before := calls
		if _, err = d.Probe(ctx, c); err != nil || calls != before+1 {
			t.Fatal("invalid cache bypassed upstream", err, calls)
		}
	}
	cache.Put(ctx, key, []byte("corrupt"), time.Minute)
	failure := errors.New("upstream unavailable")
	d.Detector = testDetector(func(context.Context, Config) (Observation, error) { return Observation{}, failure })
	if _, err := d.Probe(ctx, c); !errors.Is(err, failure) {
		t.Fatal("corrupt cache concealed upstream failure", err)
	}
	if raw, _ := cache.Get(ctx, key); string(raw) != "corrupt" {
		t.Fatal("upstream error was cached")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Probe(cancelled, c); !errors.Is(err, context.Canceled) {
		t.Fatal("cache concealed cancellation", err)
	}
}
