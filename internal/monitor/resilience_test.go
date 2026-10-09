package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestThrottlingAndWrappedPermanentClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "120"); w.WriteHeader(429) }))
	defer server.Close()
	h := NewHTTP()
	defer h.Client.CloseIdleConnections()
	err := h.JSON(context.Background(), "GET", server.URL, nil, nil, &map[string]any{})
	if !Retryable(err) || RetryDelay(err, 1, 5) < 120*time.Second {
		t.Fatal(err)
	}
	if Retryable(&ThrottledError{RemoteError: &RemoteError{"HTTP", "400", false}}) {
		t.Fatal("wrapped permanent error retried")
	}
	now := time.Now().Truncate(time.Second)
	if retryAfter(now.Add(time.Minute).UTC().Format(http.TimeFormat), now) != time.Minute || retryAfter("999999", now) != 300*time.Second || retryAfter("invalid", now) != 0 {
		t.Fatal("Retry-After parsing")
	}
	for i := 0; i < 100; i++ {
		d := RetryDelay(nil, 1, 5)
		if d < 5*time.Second || d > 6*time.Second {
			t.Fatal(d)
		}
	}
}
func TestRedisCacheFailureOpensBoundedCircuit(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond, ContextTimeoutEnabled: true})
	defer client.Close()
	cache := &RedisCache{Client: client, Prefix: "test:"}
	if _, ok := cache.Get(context.Background(), "status"); ok || cache.disabledUntil.Load() == 0 {
		t.Fatal("circuit did not open")
	}
	start := time.Now()
	for i := 0; i < 20; i++ {
		cache.Get(context.Background(), "status")
		cache.Put(context.Background(), "status", []byte("x"), time.Second)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("disabled cache delays status")
	}
}
func TestConfigRejectsTyposAndMultipleDocuments(t *testing.T) {
	original, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{string(original) + "\nmisspelled_setting: true\n", string(original) + "\n---\nsubscription: {}\n", strings.Replace(string(original), "room_id:", "rom_id:", 1)} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "config.yaml"), []byte(data), 0600)
		if _, err := Load(root); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
