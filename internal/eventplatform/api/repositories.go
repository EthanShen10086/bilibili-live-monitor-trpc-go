package api

import (
	"context"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/store"
)

// ControlRepository is the consumer's control-plane contract. Transaction and
// authorization details stay inside the owning module, outside HTTP handlers.
type ControlRepository interface {
	Tenants(context.Context, domain.Principal) ([]domain.Tenant, error)
	CreateTenant(context.Context, domain.Principal, domain.Tenant) (domain.Tenant, error)
	Members(context.Context, domain.Principal, string) ([]domain.Member, error)
	PutMember(context.Context, domain.Principal, string, domain.Member, bool) error
	Targets(context.Context, domain.Principal, string) ([]domain.Target, error)
	SaveTarget(context.Context, domain.Principal, string, domain.Target, bool) (domain.Target, error)
	Subscriptions(context.Context, domain.Principal, string) ([]domain.Subscription, error)
	SaveSubscription(context.Context, domain.Principal, string, domain.Subscription, bool) (domain.Subscription, error)
	RotateCredentials(context.Context, domain.Principal, string) error
}

type NotificationRepository interface {
	Jobs(context.Context, domain.Principal, string) ([]domain.Job, error)
	Retry(context.Context, domain.Principal, string, string) error
}

type ProjectionRepository interface {
	Statistics(context.Context, domain.Principal, string) (store.Statistics, error)
	RequestReplay(context.Context, domain.Principal, string, time.Time, time.Time, int) (store.Replay, error)
	Replays(context.Context, domain.Principal, string) ([]store.Replay, error)
}

var (
	_ ControlRepository      = store.Control{}
	_ NotificationRepository = store.Notifications{}
	_ ProjectionRepository   = store.Projections{}
)
