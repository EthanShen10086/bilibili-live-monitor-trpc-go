package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	b, e := os.ReadFile("../../config.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var c Config
	if e = yaml.Unmarshal(b, &c); e != nil {
		t.Fatal(e)
	}
	return c
}
func store(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "state.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}
func TestWindow(t *testing.T) {
	c := testConfig(t)
	for _, d := range []string{"2026-09-30", "2026-10-02", "2026-10-03", "2026-10-04"} {
		for _, p := range []struct {
			s    string
			want bool
		}{{"09:59:59", false}, {"10:00:00", true}, {"15:59:59", true}, {"16:00:00", false}} {
			v, _ := time.Parse(time.RFC3339, d+"T"+p.s+"Z")
			if c.InWindow(v) != p.want {
				t.Fatalf("window %s %s", d, p.s)
			}
		}
	}
	v, _ := time.Parse(time.RFC3339, "2026-10-01T12:00:00Z")
	if c.InWindow(v) {
		t.Fatal("Thursday active")
	}
	c.Schedule.Timezone = "not-a-zone"
	if c.Validate() == nil {
		t.Fatal("invalid zone accepted")
	}
}
func TestBackoff(t *testing.T) {
	if Backoff(1, 10) != 10*time.Second || Backoff(20, 10) != 300*time.Second {
		t.Fatal("backoff")
	}
}
func TestParseRoom(t *testing.T) {
	b := []byte(`{"code":0,"data":{"room_id":11163068,"short_id":1616,"live_status":2,"title":"rotation","live_time":"0000-00-00 00:00:00"}}`)
	o, e := ParseRoom(b, 1616, time.Now())
	if e != nil || o.Live || o.Start != "" {
		t.Fatal(o, e)
	}
	if _, e = ParseRoom(b, 999, time.Now()); e == nil || Retryable(e) {
		t.Fatal("wrong room must fail permanently")
	}
	if _, e = ParseRoom([]byte(`{"code":0,"data":{"room_id":1616}}`), 1616, time.Now()); e == nil {
		t.Fatal("schema")
	}
	if _, e = ParseRoom([]byte(`{"code":-412}`), 1616, time.Now()); e == nil || !Retryable(e) {
		t.Fatal("risk response")
	}
	if NormalizeStart("2026-10-04 20:00:00") != "2026-10-04T12:00:00.000Z" {
		t.Fatal("timezone")
	}
}
func TestDedupePersistenceLateTimestampAndNext(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "state.sqlite")
	s, e := OpenStore(file)
	if e != nil {
		t.Fatal(e)
	}
	o := Observation{RoomID: 11163068, Live: true, Title: "hello", At: 1000}
	a, e := s.Observe(o, true, 30)
	if !a || e != nil {
		t.Fatal(a, e)
	}
	j, _ := s.Due(time.UnixMilli(1000))
	if j == nil {
		t.Fatal("job missing")
	}
	s.Sent(j.Key)
	o.Start = "2026-10-04T12:00:00.000Z"
	if a, e = s.Observe(o, false, 30); a || e != nil {
		t.Fatal("late time duplicated", e)
	}
	s.DB.Close()
	s, e = OpenStore(file)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	o.Title = "changed"
	if a, e = s.Observe(o, true, 30); a || e != nil {
		t.Fatal("restart duplicated")
	}
	o.Start = "2026-10-04T13:00:00.000Z"
	if a, e = s.Observe(o, false, 30); !a || e != nil {
		t.Fatal("next timestamp not queued")
	}
	o.Live = false
	s.Observe(o, false, 30)
	o.Live = true
	o.Start = ""
	if a, e = s.Observe(o, false, 30); !a || e != nil {
		t.Fatal("transition missing")
	}
}
func TestQueueTTLAndRetry(t *testing.T) {
	s := store(t)
	o := Observation{RoomID: 1, Live: true, At: 1000}
	s.Observe(o, false, 1)
	j, _ := s.Due(time.UnixMilli(1000))
	s.Failed(j, &RemoteError{"Feishu", "500", true}, time.UnixMilli(1000))
	j, _ = s.Due(time.UnixMilli(2000))
	if j != nil {
		t.Fatal("retry early")
	}
	j, _ = s.Due(time.UnixMilli(6000))
	if j == nil || j.Attempts != 1 {
		t.Fatal("retry missing")
	}
	s.Failed(j, &RemoteError{"Feishu", "19024", false}, time.UnixMilli(6000))
	n, _ := s.Retry(time.UnixMilli(7000))
	if n != 1 {
		t.Fatal("failed retry")
	}
	j, _ = s.Due(time.UnixMilli(61000))
	if j != nil {
		t.Fatal("TTL boundary")
	}
	n, _ = s.Retry(time.UnixMilli(62000))
	if n != 0 {
		t.Fatal("expired requeue")
	}
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func mockHTTP(f transport) *HTTP {
	return &HTTP{Client: &http.Client{Transport: f, Timeout: time.Second}}
}
func TestWebhookSignatureAndBusinessError(t *testing.T) {
	c := testConfig(t)
	t.Setenv(c.Notification.Group.Webhook, "https://open.feishu.cn/open-apis/bot/v2/hook/test")
	t.Setenv(c.Notification.Group.Secret, "unit-secret")
	calls := 0
	f := Feishu{Config: c, HTTP: mockHTTP(func(r *http.Request) (*http.Response, error) {
		calls++
		var p struct {
			Timestamp string `json:"timestamp"`
			Sign      string `json:"sign"`
		}
		json.NewDecoder(r.Body).Decode(&p)
		if p.Sign != FeishuSign(p.Timestamp, "unit-secret") {
			t.Fatal("signature")
		}
		if calls == 1 {
			return response(`{"code":0}`, 200), nil
		}
		return response(`{"code":19024}`, 200), nil
	})}
	if e := f.Send(context.Background(), "test", "1"); e != nil {
		t.Fatal(e)
	}
	if e := f.Send(context.Background(), "test", "2"); e == nil || Retryable(e) {
		t.Fatal("permanent business error")
	}
	if FeishuSign("123", "key") != "WfZl7dVEf9ZNUz5G4nBh2cVYOugajN+iNene0QAcYVY=" {
		t.Fatal("signature vector")
	}
}
func TestPrivateTokenRefresh(t *testing.T) {
	c := testConfig(t)
	c.Notification.Mode = "feishu_private"
	tokens, sends := 0, 0
	f := Feishu{Config: c, HTTP: mockHTTP(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			tokens++
			return response(fmt.Sprintf(`{"code":0,"tenant_access_token":"token%d","expire":7200}`, tokens), 200), nil
		}
		sends++
		if sends == 1 {
			return response(`{"code":99991661}`, 200), nil
		}
		return response(`{"code":0}`, 200), nil
	})}
	if e := f.Send(context.Background(), "hello", "key"); e != nil {
		t.Fatal(e)
	}
	if e := f.Send(context.Background(), "next", "key2"); e != nil {
		t.Fatal(e)
	}
	if tokens != 2 || sends != 3 {
		t.Fatal(tokens, sends)
	}
}
func TestLockInterop(t *testing.T) {
	dir := t.TempDir()
	release, e := Lock(dir, "instance")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Lock(dir, "instance"); e == nil {
		t.Fatal("duplicate lock")
	}
	if _, e = os.Stat(filepath.Join(dir, "var/instance.guard.lock")); e != nil {
		t.Fatal("Node lock path mismatch")
	}
	release()
	os.Mkdir(filepath.Join(dir, "var/instance.guard.lock"), 0700)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(dir, "var/instance.guard.lock"), old, old)
	release, e = Lock(dir, "instance")
	if e != nil {
		t.Fatal(e)
	}
	release()
}
func TestEnvPermissionsAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	os.WriteFile(p, []byte("MONITOR_TEST_ENV=file\n"), 0644)
	if LoadEnv(dir) == nil {
		t.Fatal("permissions")
	}
	os.Chmod(p, 0600)
	t.Setenv("MONITOR_TEST_ENV", "shell")
	if e := LoadEnv(dir); e != nil || os.Getenv("MONITOR_TEST_ENV") != "shell" {
		t.Fatal(e)
	}
}
func TestServiceTemplates(t *testing.T) {
	p, u := ServiceFiles(`/tmp/a & "b"`, "/bin/monitor")
	if !strings.Contains(p, "&amp;") || !strings.Contains(p, "KeepAlive") || !strings.Contains(u, "Restart=on-failure") || !strings.Contains(u, "TimeoutStopSec=80") || !strings.Contains(u, "StartLimitIntervalSec=0") {
		t.Fatal("templates")
	}
}
func TestWorkerNoNetworkOutsideWindow(t *testing.T) {
	c := testConfig(t)
	c.Schedule.Weekdays = []int{int(time.Now().Weekday())%7 + 1}
	if c.InWindow(time.Now()) {
		c.Schedule.Weekdays = []int{(c.Schedule.Weekdays[0] % 7) + 1}
	}
	t.Setenv(c.Notification.Group.Webhook, "https://open.feishu.cn/open-apis/bot/v2/hook/test")
	t.Setenv(c.Notification.Group.Secret, "test")
	var calls atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	root := t.TempDir()
	e := Run(ctx, root, c, mockHTTP(func(r *http.Request) (*http.Response, error) { calls.Add(1); return response(`{"code":0}`, 200), nil }))
	if e != nil || calls.Load() != 0 {
		t.Fatal(e, calls.Load())
	}
	s, _ := ReadStatus(root)
	if s.Running {
		t.Fatal("shutdown state")
	}
}
func TestWorkerDurableSendAndRestart(t *testing.T) {
	c := testConfig(t)
	c.Schedule.Weekdays = []int{1, 2, 3, 4, 5, 6, 7}
	c.Schedule.Start = "00:00"
	c.Schedule.End = "24:00"
	c.Detector.Polling.IntervalMinutes = nil
	legacyInterval := 1
	c.Detector.Polling.Interval = &legacyInterval
	t.Setenv(c.Notification.Group.Webhook, "https://open.feishu.cn/open-apis/bot/v2/hook/test")
	t.Setenv(c.Notification.Group.Secret, "test")
	var sends atomic.Int32
	h := mockHTTP(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.live.bilibili.com" {
			return response(`{"code":0,"data":{"room_id":11163068,"short_id":1616,"live_status":1,"title":"hello","live_time":"2026-10-04 20:00:00"}}`, 200), nil
		}
		sends.Add(1)
		return response(`{"code":0}`, 200), nil
	})
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
		e := Run(ctx, root, c, h)
		cancel()
		if e != nil {
			t.Fatal(e)
		}
		state, se := ReadStatus(root)
		if se != nil || state.PollingPhase != "notified_live" || state.EffectiveInterval != 300 || state.NextPoll-state.LastObservation < 300000 {
			t.Fatal("adaptive state after send/restart", state, se)
		}
	}
	if sends.Load() != 1 {
		t.Fatal("duplicate after restart", sends.Load())
	}
}

func TestNodeStatusPendingArrayCompatibility(t *testing.T) {
	root := t.TempDir()
	if e := AtomicJSON(filepath.Join(root, "var/status.json"), map[string]any{"pid": 123, "running": false, "pending": []any{map[string]any{"state": "pending", "count": 1}}}); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadStatus(root); e != nil {
		t.Fatal(e)
	}
}

func TestMinutePollingValidationAndBackoff(t *testing.T) {
	c := testConfig(t)
	if e := c.Validate(); e != nil || c.PollingSeconds() != 60 {
		t.Fatal(e, c.PollingSeconds())
	}
	five := 5
	c.Detector.Polling.IntervalMinutes = &five
	if e := c.Validate(); e != nil || c.PollingSeconds() != 300 {
		t.Fatal(e, c.PollingSeconds())
	}
	legacy := 10
	c.Detector.Polling.Interval = &legacy
	if c.Validate() == nil {
		t.Fatal("ambiguous units accepted")
	}
	c.Detector.Polling.IntervalMinutes = nil
	if e := c.Validate(); e != nil || c.PollingSeconds() != 10 {
		t.Fatal(e, c.PollingSeconds())
	}
	c.Detector.Polling.Interval = nil
	if c.Validate() == nil {
		t.Fatal("missing interval accepted")
	}
	for _, value := range []int{0, -1, 61} {
		c.Detector.Polling.IntervalMinutes = &value
		if c.Validate() == nil {
			t.Fatal("invalid minutes accepted", value)
		}
	}
	if Backoff(1, 60) != time.Minute || Backoff(3, 60) != 4*time.Minute || Backoff(4, 60) != 5*time.Minute || Backoff(5, 600) != 10*time.Minute {
		t.Fatal("minute retry intervals")
	}
}

func TestMinuteWorkerSchedulesWithoutRepeatedQueries(t *testing.T) {
	c := testConfig(t)
	c.Schedule.Weekdays = []int{1, 2, 3, 4, 5, 6, 7}
	c.Schedule.Start = "00:00"
	c.Schedule.End = "24:00"
	t.Setenv(c.Notification.Group.Webhook, "https://open.feishu.cn/open-apis/bot/v2/hook/test")
	t.Setenv(c.Notification.Group.Secret, "unit-secret")
	var queries atomic.Int32
	h := mockHTTP(func(r *http.Request) (*http.Response, error) {
		queries.Add(1)
		return response(`{"code":0,"data":{"room_id":11163068,"short_id":1616,"live_status":0,"title":"minute test","live_time":""}}`, 200), nil
	})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	if e := Run(ctx, root, c, h); e != nil {
		t.Fatal(e)
	}
	if queries.Load() != 1 {
		t.Fatal("unexpected repeated query", queries.Load())
	}
	status, e := ReadStatus(root)
	if e != nil || status.PollingInterval != 60 || status.NextPoll-status.LastObservation < 60000 {
		t.Fatal(status, e)
	}
}

func TestWorkerSendFailureKeepsFastPolling(t *testing.T) {
	c := testConfig(t)
	c.Schedule.Weekdays = []int{1, 2, 3, 4, 5, 6, 7}
	c.Schedule.Start = "00:00"
	c.Schedule.End = "24:00"
	t.Setenv(c.Notification.Group.Webhook, "https://open.feishu.cn/open-apis/bot/v2/hook/test")
	t.Setenv(c.Notification.Group.Secret, "unit")
	h := mockHTTP(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.live.bilibili.com" {
			return response(`{"code":0,"data":{"room_id":11163068,"short_id":1616,"live_status":1,"title":"test","live_time":"2026-10-04 20:00:00"}}`, 200), nil
		}
		return response("retry", 503), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	root := t.TempDir()
	if e := Run(ctx, root, c, h); e != nil {
		t.Fatal(e)
	}
	s, e := ReadStatus(root)
	if e != nil || s.PollingPhase != "awaiting_notification" || s.EffectiveInterval != 60 || s.LastSent != 0 {
		t.Fatal(s, e)
	}
}
