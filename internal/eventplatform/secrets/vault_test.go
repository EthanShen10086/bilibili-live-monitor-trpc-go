package secrets

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
)

func TestEncryptionRotationAndTenantBinding(t *testing.T) {
	k1 := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	k2 := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	v, e := New("old", map[string]string{"old": k1})
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := v.Seal("tenant-a:target", domain.Credentials{Secret: "private-secret"})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(encoded, "private-secret") {
		t.Fatal("plaintext persisted")
	}
	if _, e = v.Open("tenant-b:target", encoded); e == nil {
		t.Fatal("cross-tenant credential access")
	}
	rotated, e := New("new", map[string]string{"old": k1, "new": k2})
	if e != nil {
		t.Fatal(e)
	}
	creds, e := rotated.Open("tenant-a:target", encoded)
	if e != nil || creds.Secret != "private-secret" {
		t.Fatal("old key unreadable")
	}
	fresh, e := rotated.Seal("tenant-a:target", creds)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(fresh, "new:") {
		t.Fatal("rotation used old key")
	}
}
