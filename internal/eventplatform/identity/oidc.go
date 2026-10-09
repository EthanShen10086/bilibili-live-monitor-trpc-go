// Package identity authenticates API callers without granting tenant resource access.
package identity

import (
	"context"
	"errors"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
)

type Verifier interface {
	Verify(context.Context, string) (domain.Principal, error)
}
type OIDC struct {
	verifier *oidc.IDTokenVerifier
	admins   map[string]bool
}

func New(ctx context.Context, issuer, audience string, admins []string) (*OIDC, error) {
	if issuer == "" || audience == "" {
		return nil, errors.New("OIDC issuer and audience required")
	}
	provider, e := oidc.NewProvider(ctx, issuer)
	if e != nil {
		return nil, errors.New("OIDC discovery unavailable")
	}
	v := &OIDC{provider.Verifier(&oidc.Config{ClientID: audience}), map[string]bool{}}
	for _, id := range admins {
		v.admins[id] = true
	}
	return v, nil
}

func (v *OIDC) Verify(ctx context.Context, header string) (domain.Principal, error) {
	prefix, token, ok := strings.Cut(header, " ")
	if !ok || prefix != "Bearer" || token == "" || len(token) > 16384 {
		return domain.Principal{}, domain.ErrForbidden
	}
	id, e := v.verifier.Verify(ctx, token)
	if e != nil || id.Subject == "" {
		return domain.Principal{}, domain.ErrForbidden
	}
	return domain.Principal{Subject: id.Subject, PlatformAdmin: v.admins[id.Subject]}, nil
}
