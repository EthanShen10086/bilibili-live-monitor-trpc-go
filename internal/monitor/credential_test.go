package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestFeishuCredentialsRemainInstanceLocal(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		seen[body.Content.Text] = true
		mu.Unlock()
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()
	t.Setenv("GROUP_WEBHOOK", "http://invalid.example")
	var c Config
	c.Notification.Mode = "feishu_group"
	c.Notification.Group.Webhook = "GROUP_WEBHOOK"
	c.Notification.Group.Secret = "GROUP_SECRET"
	for _, text := range []string{"tenant-a", "tenant-b"} {
		f := &Feishu{Config: c, HTTP: NewHTTP(), Lookup: func(name string) string {
			if name == "GROUP_WEBHOOK" {
				return server.URL
			}
			return "test-only"
		}}
		if err := f.Send(context.Background(), text, text); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("expected independent targets: %v", seen)
	}
}
