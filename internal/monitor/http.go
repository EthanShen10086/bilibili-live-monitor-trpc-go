package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type RemoteError struct {
	Service, Code string
	Retry         bool
}

func (e *RemoteError) Error() string { return e.Service + ": " + e.Code }
func Retryable(e error) bool {
	if r, ok := e.(*RemoteError); ok {
		return r.Retry
	}
	return true
}

type HTTP struct{ Client *http.Client }

func NewHTTP() *HTTP { return &HTTP{Client: &http.Client{Timeout: 10 * time.Second}} }
func (h *HTTP) JSON(ctx context.Context, method, url string, body []byte, headers map[string]string, out any) error {
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
		return &RemoteError{"HTTP", fmt.Sprint(r.StatusCode), r.StatusCode == 429 || r.StatusCode >= 500}
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
