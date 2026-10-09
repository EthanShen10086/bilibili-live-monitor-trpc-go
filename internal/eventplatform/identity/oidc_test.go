package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestOIDCSignatureAudienceExpiryAndKeyRotation(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	current := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "first", Algorithm: "RS256", Use: "sig"}
	issuer := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/.well-known/openid-configuration" {
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{current}})
	}))
	defer server.Close()
	issuer = server.URL
	v, e := New(context.Background(), issuer, "live-api", []string{"owner"})
	if e != nil {
		t.Fatal(e)
	}
	sign := func(key *rsa.PrivateKey, kid, audience string, expires time.Time) string {
		signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", kid))
		if e != nil {
			t.Fatal(e)
		}
		token, e := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: "owner", Audience: jwt.Audience{audience}, Expiry: jwt.NewNumericDate(expires), IssuedAt: jwt.NewNumericDate(time.Now())}).Serialize()
		if e != nil {
			t.Fatal(e)
		}
		return "Bearer " + token
	}
	p, e := v.Verify(context.Background(), sign(key, "first", "live-api", time.Now().Add(time.Minute)))
	if e != nil || !p.PlatformAdmin {
		t.Fatal("valid token denied", e)
	}
	for _, token := range []string{"Bearer garbage", sign(key, "first", "wrong-api", time.Now().Add(time.Minute)), sign(key, "first", "live-api", time.Now().Add(-time.Minute))} {
		if _, e = v.Verify(context.Background(), token); e == nil {
			t.Fatal("invalid token accepted")
		}
	}
	second, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	current = jose.JSONWebKey{Key: &second.PublicKey, KeyID: "second", Algorithm: "RS256", Use: "sig"}
	mu.Unlock()
	if _, e = v.Verify(context.Background(), sign(second, "second", "live-api", time.Now().Add(time.Minute))); e != nil {
		t.Fatal("rotated JWKS not refreshed", e)
	}
}
