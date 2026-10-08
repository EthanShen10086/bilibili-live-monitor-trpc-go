package monitor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type measuredBody struct {
	io.Reader
	bytes  int
	closed bool
}

func (b *measuredBody) Read(p []byte) (int, error) {
	n, e := b.Reader.Read(p)
	b.bytes += n
	return n, e
}
func (b *measuredBody) Close() error { b.closed = true; return nil }
func TestHTTPBoundsAndClosesBodies(t *testing.T) {
	for _, status := range []int{429, 200} {
		body := &measuredBody{Reader: strings.NewReader(strings.Repeat("x", 3*1024*1024))}
		h := mockHTTP(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: body, Header: make(http.Header)}, nil
		})
		var out any
		e := h.JSON(context.Background(), "GET", "https://example.com", nil, nil, &out)
		if e == nil || !body.closed || body.bytes > 2*1024*1024+1 {
			t.Fatal(e, body.bytes, body.closed)
		}
	}
}
