package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
)

type testVerifier struct{}

func (testVerifier) Verify(_ context.Context, header string) (domain.Principal, error) {
	if header == "Bearer test" {
		return domain.Principal{Subject: "verified"}, nil
	}
	return domain.Principal{}, domain.ErrForbidden
}

func TestInternalAuthenticationRejectsSpoofedIdentity(t *testing.T) {
	s := Server{Verify: testVerifier{}}
	handler := s.Handler()
	for _, auth := range []string{"", "Bearer test"} {
		r := httptest.NewRequest(http.MethodGet, "/internal/auth", nil)
		r.Header.Set("X-User", "admin")
		r.Header.Set("X-Tenant-ID", "other")
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if auth != "" {
			want = http.StatusOK
		}
		if w.Code != want {
			t.Fatal(w.Code)
		}
		if strings.Contains(w.Body.String(), "admin") {
			t.Fatal("spoofed identity accepted")
		}
	}
}

func TestStrictBodyAndCredentialResponse(t *testing.T) {
	for _, body := range []string{`{"unknown":1}`, `{} {}`, strings.Repeat("x", 70000)} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if decode(httptest.NewRecorder(), r, &domain.Tenant{}) == nil {
			t.Fatal("invalid body accepted")
		}
	}
	views := targetViews([]domain.Target{{ID: "target", Credentials: domain.Credentials{Secret: "hidden"}}})
	if _, ok := views[0]["credentials"]; ok {
		t.Fatal("credential field exposed")
	}
}
