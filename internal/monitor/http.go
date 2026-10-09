package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type RemoteError struct {
	Service, Code string
	Retry         bool
}

func (e *RemoteError) Error() string { return e.Service + ": " + e.Code }
func Retryable(e error) bool {
	var r *RemoteError
	if errors.As(e, &r) {
		return r.Retry
	}
	return true
}

type HTTP struct {
	Client   *http.Client
	observer Observer
}

func (h *HTTP) SetObserver(o Observer) { h.observer = o }

func NewHTTP() *HTTP { return &HTTP{Client: &http.Client{Timeout: 10 * time.Second}} }
func (h *HTTP) JSON(ctx context.Context, method, url string, body []byte, headers map[string]string, out any) (err error) {
	if h.observer != nil {
		var end func(error)
		ctx, end = h.observer.Begin(ctx, "http")
		defer func() { end(err) }()
	}
	req, e := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if e != nil {
		return &RemoteError{"HTTP", "request", false}
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r, e := h.Client.Do(req)
	if e != nil {
		return &RemoteError{"HTTP", "network_or_timeout", true}
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		remote := &RemoteError{"HTTP", fmt.Sprint(r.StatusCode), r.StatusCode == 429 || r.StatusCode >= 500}
		if r.StatusCode == 429 || r.StatusCode == 503 {
			return &ThrottledError{RemoteError: remote, After: retryAfter(r.Header.Get("Retry-After"), time.Now())}
		}
		return remote
	}
	data, e := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024+1))
	if e != nil || len(data) > 2*1024*1024 {
		return &RemoteError{"HTTP", "response_limit", true}
	}
	if json.Unmarshal(data, out) != nil {
		return &RemoteError{"HTTP", "invalid_json", true}
	}
	return nil
}
func jsonBody(v any) []byte { b, _ := json.Marshal(v); return b }

// ThrottledError preserves safe provider classification and the server's retry budget.
type ThrottledError struct {
	*RemoteError
	After time.Duration
}

func (e *ThrottledError) Unwrap() error { return e.RemoteError }
func retryAfter(value string, now time.Time) time.Duration {
	var delay time.Duration
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil {
		delay = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(value); err == nil {
		delay = date.Sub(now)
	}
	if delay < 0 {
		return 0
	}
	if delay > 300*time.Second {
		return 300 * time.Second
	}
	return delay
}

// Positive jitter spreads replicas without retrying sooner than the normal backoff.
func RetryDelay(err error, attempt, base int) time.Duration {
	delay := Backoff(attempt, base)
	delay += time.Duration(rand.Float64() * float64(delay) / 5)
	ceiling := max(300*time.Second, time.Duration(base)*time.Second)
	if delay > ceiling {
		delay = ceiling
	}
	var throttled *ThrottledError
	if errors.As(err, &throttled) && throttled.After > delay {
		delay = throttled.After
	}
	return delay
}
