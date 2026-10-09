package monitor

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Cache interface {
	Get(context.Context, string) ([]byte, bool)
	Put(context.Context, string, []byte, time.Duration)
	Close() error
}
type cacheEntry struct {
	data  []byte
	until time.Time
}
type MemoryCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

func NewMemoryCache() *MemoryCache { return &MemoryCache{entries: map[string]cacheEntry{}} }
func (c *MemoryCache) Get(_ context.Context, k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || time.Now().After(e.until) {
		delete(c.entries, k)
		return nil, false
	}
	return append([]byte(nil), e.data...), true
}
func (c *MemoryCache) Put(_ context.Context, k string, b []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= 64 {
		for key := range c.entries {
			delete(c.entries, key)
		}
	}
	c.entries[k] = cacheEntry{append([]byte(nil), b...), time.Now().Add(ttl)}
}
func (c *MemoryCache) Close() error { return nil }

type RedisCache struct {
	Client *redis.Client
	Prefix string
}

func redisClient(c Config) (*redis.Client, error) {
	opts, e := redis.ParseURL(os.Getenv(c.Platform.Redis.URLEnv))
	if e != nil {
		return nil, fmt.Errorf("Redis URL invalid")
	}
	opts.DialTimeout = 2 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second
	opts.PoolSize = 4
	opts.MaxRetries = 0
	opts.DialerRetries = 1
	opts.ContextTimeoutEnabled = true
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e = client.Ping(ctx).Err(); e != nil {
		client.Close()
		return nil, fmt.Errorf("Redis connection failed")
	}
	return client, nil
}
func OpenCache(c Config) (Cache, error) {
	if c.Platform.CacheMode() == "memory" {
		return NewMemoryCache(), nil
	}
	client, e := redisClient(c)
	if e != nil {
		return nil, e
	}
	return &RedisCache{client, "live-monitor:" + c.Platform.SubscriptionID + ":cache:"}, nil
}
func (c *RedisCache) Get(ctx context.Context, k string) ([]byte, bool) {
	cc, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	b, e := c.Client.Get(cc, c.Prefix+k).Bytes()
	if e != nil || len(b) > 64*1024 {
		return nil, false
	}
	return b, true
}
func (c *RedisCache) Put(ctx context.Context, k string, b []byte, ttl time.Duration) {
	if len(b) > 64*1024 {
		return
	}
	cc, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c.Client.Set(cc, c.Prefix+k, b, ttl)
}
func (c *RedisCache) Close() error { return c.Client.Close() }

type StreamQueue struct {
	Client           *redis.Client
	Store            *PostgresStore
	Stream, Consumer string
}

func OpenStreamQueue(c Config, s *PostgresStore) (*StreamQueue, error) {
	client, e := redisClient(c)
	if e != nil {
		return nil, e
	}
	q := &StreamQueue{client, s, "live-monitor:" + s.Scope + ":notifications", s.Owner}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e = client.XGroupCreateMkStream(ctx, q.Stream, "senders", "0").Err()
	if e != nil && e.Error() != "BUSYGROUP Consumer Group name already exists" {
		client.Close()
		return nil, fmt.Errorf("Redis consumer group setup failed")
	}
	return q, nil
}
func (q *StreamQueue) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q.Client.XGroupDelConsumer(ctx, q.Stream, "senders", q.Consumer)
	return q.Client.Close()
}
func (q *StreamQueue) Publish(ctx context.Context) error {
	ctx, cancelPulse := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPulse()
	outbox, e := q.Store.Outbox()
	if e != nil {
		return e
	}
	for key := range outbox {
		if ctx.Err() != nil {
			return fmt.Errorf("Redis publish budget exhausted")
		}
		cc, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, e = q.Client.XAdd(cc, &redis.XAddArgs{Stream: q.Stream, MaxLen: 10000, Approx: true, Values: map[string]any{"key": key}}).Result()
		cancel()
		if e != nil {
			return fmt.Errorf("Redis publish unavailable")
		}
		if e = q.Store.Published(key); e != nil {
			return e
		}
	}
	return nil
}

// Stream messages are durable wakeups. PostgreSQL claims and periodic scans govern actual sends.
func (q *StreamQueue) Receive(ctx context.Context) (*Job, string, error) {
	cc, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	streams, e := q.Client.XReadGroup(cc, &redis.XReadGroupArgs{Group: "senders", Consumer: q.Consumer, Streams: []string{q.Stream, ">"}, Count: 1, Block: -1}).Result()
	var messages []redis.XMessage
	if e == nil && len(streams) > 0 {
		messages = streams[0].Messages
	} else if e != nil && e != redis.Nil {
		return nil, "", fmt.Errorf("Redis receive unavailable")
	}
	if len(messages) == 0 {
		messages, _, e = q.Client.XAutoClaim(cc, &redis.XAutoClaimArgs{Stream: q.Stream, Group: "senders", Consumer: q.Consumer, MinIdle: 60 * time.Second, Start: "0-0", Count: 1}).Result()
		if e != nil && e != redis.Nil {
			return nil, "", fmt.Errorf("Redis reclaim unavailable")
		}
	}
	if len(messages) == 0 {
		return nil, "", nil
	}
	m := messages[0]
	key, ok := m.Values["key"].(string)
	if !ok || len(key) > 512 {
		q.Ack(ctx, m.ID)
		return nil, "", nil
	}
	job, e := q.Store.ClaimKey(key)
	if e != nil {
		return nil, "", e
	}
	if job == nil {
		return nil, "", q.Ack(ctx, m.ID)
	}
	return job, m.ID, nil
}
func (q *StreamQueue) Ack(ctx context.Context, id string) error {
	cc, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if e := q.Client.XAck(cc, q.Stream, "senders", id).Err(); e != nil {
		return fmt.Errorf("Redis acknowledgement unavailable")
	}
	// One consumer group per stream; delete confirmed wakeups without growing a history log.
	return q.Client.XDel(cc, q.Stream, id).Err()
}
