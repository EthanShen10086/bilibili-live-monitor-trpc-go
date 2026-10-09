// Package secrets encrypts tenant channel credentials with versioned, rotating keys.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
)

type Vault struct {
	Current string
	Keys    map[string][]byte
}

func New(current string, keys map[string]string) (*Vault, error) {
	v := &Vault{Current: current, Keys: map[string][]byte{}}
	for id, encoded := range keys {
		key, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil || len(key) != 32 || id == "" || strings.Contains(id, ":") {
			return nil, errors.New("invalid encryption key configuration")
		}
		v.Keys[id] = key
	}
	if len(v.Keys[current]) != 32 {
		return nil, errors.New("missing current encryption key")
	}
	return v, nil
}

func (v *Vault) Seal(scope string, c domain.Credentials) (string, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	block, e := aes.NewCipher(v.Keys[v.Current])
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return v.Current + ":" + base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, b, []byte(scope))), nil
}

func (v *Vault) Open(scope, encoded string) (domain.Credentials, error) {
	var c domain.Credentials
	id, raw, ok := strings.Cut(encoded, ":")
	if !ok || len(v.Keys[id]) != 32 {
		return c, errors.New("credential key unavailable")
	}
	b, e := base64.StdEncoding.DecodeString(raw)
	if e != nil {
		return c, errors.New("invalid encrypted credential")
	}
	block, e := aes.NewCipher(v.Keys[id])
	if e != nil {
		return c, e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return c, e
	}
	if len(b) < g.NonceSize() {
		return c, errors.New("invalid encrypted credential")
	}
	plain, e := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], []byte(scope))
	if e != nil {
		return c, errors.New("credential integrity check failed")
	}
	if e = json.Unmarshal(plain, &c); e != nil {
		return domain.Credentials{}, errors.New("invalid credential payload")
	}
	return c, nil
}
