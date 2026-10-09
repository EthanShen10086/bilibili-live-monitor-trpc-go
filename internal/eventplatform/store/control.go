package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"
)

// Control owns tenants, memberships, subscriptions, targets and management audit.
type (
	Control struct{ DB *DB }
	querier interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}
)

func authorize(ctx context.Context, q querier, p domain.Principal, tenant, action string) error {
	if p.PlatformAdmin {
		return nil
	}
	var role string
	if e := q.QueryRowContext(ctx, "SELECT role FROM ep_members WHERE tenant_id=$1 AND subject=$2", tenant, p.Subject).Scan(&role); e != nil || !domain.Can(role, action) {
		return domain.ErrForbidden
	}
	return nil
}

func audit(ctx context.Context, tx *sql.Tx, p domain.Principal, tenant, action, id string) error {
	_, e := tx.ExecContext(ctx, "INSERT INTO ep_audit(tenant_id,subject,action,resource_id) VALUES($1,$2,$3,$4)", tenant, p.Subject, action, id)
	return e
}

func (c Control) Tenants(ctx context.Context, p domain.Principal) ([]domain.Tenant, error) {
	rows, e := c.DB.SQL.QueryContext(ctx, "SELECT t.id,t.name,t.max_subscriptions,t.max_targets FROM ep_tenants t WHERE $1 OR EXISTS(SELECT 1 FROM ep_members m WHERE m.tenant_id=t.id AND m.subject=$2) ORDER BY t.id LIMIT 1000", p.PlatformAdmin, p.Subject)
	if e != nil {
		return nil, e
	}
	defer resource.Close(rows)
	out := []domain.Tenant{}
	for rows.Next() {
		var t domain.Tenant
		if e = rows.Scan(&t.ID, &t.Name, &t.MaxSubscriptions, &t.MaxTargets); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (c Control) CreateTenant(ctx context.Context, p domain.Principal, t domain.Tenant) (domain.Tenant, error) {
	if !p.PlatformAdmin {
		return t, domain.ErrForbidden
	}
	t.ID = monitor.ID()
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || len(t.Name) > 100 {
		return t, domain.ErrInvalid
	}
	if t.MaxSubscriptions == 0 {
		t.MaxSubscriptions = 100
	}
	if t.MaxTargets == 0 {
		t.MaxTargets = 100
	}
	e := transaction(ctx, c.DB.SQL, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO ep_tenants VALUES($1,$2,$3,$4)", t.ID, t.Name, t.MaxSubscriptions, t.MaxTargets); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO ep_members VALUES($1,$2,'owner')", t.ID, p.Subject); e != nil {
			return e
		}
		return audit(ctx, tx, p, t.ID, "tenant.create", t.ID)
	})
	return t, e
}

func lockTenant(ctx context.Context, tx *sql.Tx, tenant string) (domain.Tenant, error) {
	var t domain.Tenant
	t.ID = tenant
	e := tx.QueryRowContext(ctx, "SELECT name,max_subscriptions,max_targets FROM ep_tenants WHERE id=$1 FOR UPDATE", tenant).Scan(&t.Name, &t.MaxSubscriptions, &t.MaxTargets)
	return t, e
}

func (c Control) Members(ctx context.Context, p domain.Principal, tenant string) ([]domain.Member, error) {
	if e := authorize(ctx, c.DB.SQL, p, tenant, "read"); e != nil {
		return nil, e
	}
	rows, e := c.DB.SQL.QueryContext(ctx, "SELECT subject,role FROM ep_members WHERE tenant_id=$1 ORDER BY subject LIMIT 1000", tenant)
	if e != nil {
		return nil, e
	}
	defer resource.Close(rows)
	out := []domain.Member{}
	for rows.Next() {
		var m domain.Member
		if e = rows.Scan(&m.Subject, &m.Role); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (c Control) PutMember(ctx context.Context, p domain.Principal, tenant string, m domain.Member, remove bool) error {
	if m.Subject == "" || len(m.Subject) > 256 || (!remove && !domain.ValidRole(m.Role)) {
		return domain.ErrInvalid
	}
	return transaction(ctx, c.DB.SQL, func(tx *sql.Tx) error {
		if e := authorize(ctx, tx, p, tenant, "ownership"); e != nil {
			return e
		}
		if _, e := lockTenant(ctx, tx, tenant); e != nil {
			return e
		}
		var owners int
		var previous string
		e := tx.QueryRowContext(ctx, "SELECT role FROM ep_members WHERE tenant_id=$1 AND subject=$2", tenant, m.Subject).Scan(&previous)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if previous == "owner" && (remove || m.Role != "owner") {
			if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM ep_members WHERE tenant_id=$1 AND role='owner'", tenant).Scan(&owners); e != nil {
				return e
			}
			if owners <= 1 {
				return domain.ErrConflict
			}
		}
		if remove {
			_, e = tx.ExecContext(ctx, "DELETE FROM ep_members WHERE tenant_id=$1 AND subject=$2", tenant, m.Subject)
		} else {
			_, e = tx.ExecContext(ctx, "INSERT INTO ep_members VALUES($1,$2,$3) ON CONFLICT(tenant_id,subject) DO UPDATE SET role=excluded.role", tenant, m.Subject, m.Role)
		}
		if e != nil {
			return e
		}
		return audit(ctx, tx, p, tenant, "member.update", m.Subject)
	})
}

func (c Control) Targets(ctx context.Context, p domain.Principal, tenant string) ([]domain.Target, error) {
	if e := authorize(ctx, c.DB.SQL, p, tenant, "read"); e != nil {
		return nil, e
	}
	rows, e := c.DB.SQL.QueryContext(ctx, "SELECT id,name,kind,version FROM ep_targets WHERE tenant_id=$1 ORDER BY id LIMIT 1000", tenant)
	if e != nil {
		return nil, e
	}
	defer resource.Close(rows)
	out := []domain.Target{}
	for rows.Next() {
		var t domain.Target
		t.TenantID = tenant
		if e = rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Version); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (c Control) SaveTarget(ctx context.Context, p domain.Principal, tenant string, t domain.Target, create bool) (domain.Target, error) {
	if e := t.Validate(); e != nil {
		return t, e
	}
	t.TenantID = tenant
	if create {
		t.ID = monitor.ID()
		t.Version = 1
	}
	encrypted, e := c.DB.Vault.Seal(tenant+":"+t.ID, t.Credentials)
	if e != nil {
		return t, e
	}
	e = transaction(ctx, c.DB.SQL, func(tx *sql.Tx) error {
		if e := authorize(ctx, tx, p, tenant, "manage"); e != nil {
			return e
		}
		limit, e := lockTenant(ctx, tx, tenant)
		if e != nil {
			return e
		}
		if create {
			var count int
			if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM ep_targets WHERE tenant_id=$1", tenant).Scan(&count); e != nil {
				return e
			}
			if count >= limit.MaxTargets {
				return domain.ErrQuota
			}
			_, e = tx.ExecContext(ctx, "INSERT INTO ep_targets(tenant_id,id,name,kind,credentials) VALUES($1,$2,$3,$4,$5)", tenant, t.ID, t.Name, t.Kind, encrypted)
		} else {
			e = affected(tx.ExecContext(ctx, "UPDATE ep_targets SET name=$3,kind=$4,credentials=$5,version=version+1 WHERE tenant_id=$1 AND id=$2 AND version=$6", tenant, t.ID, t.Name, t.Kind, encrypted, t.Version))
			if e == nil {
				t.Version++
			}
		}
		if e != nil {
			return e
		}
		return audit(ctx, tx, p, tenant, "target.save", t.ID)
	})
	t.Credentials = domain.Credentials{}
	return t, e
}

func subscriptions(ctx context.Context, db *sql.DB, tenant string, room int64) ([]domain.Subscription, error) {
	rows, e := db.QueryContext(ctx, "SELECT tenant_id,id,room,enabled,policy,version,created_at FROM ep_subscriptions WHERE ($1='' OR tenant_id=$1) AND ($2::bigint=0 OR (room=$2 AND enabled)) ORDER BY tenant_id,id LIMIT 10000", tenant, room)
	if e != nil {
		return nil, e
	}
	out := []domain.Subscription{}
	for rows.Next() {
		var s domain.Subscription
		var raw []byte
		if e = rows.Scan(&s.TenantID, &s.ID, &s.RoomID, &s.Enabled, &raw, &s.Version, &s.CreatedAt); e != nil {
			resource.Close(rows)
			return nil, e
		}
		if e = json.Unmarshal(raw, &s.Policy); e != nil {
			resource.Close(rows)
			return nil, e
		}
		out = append(out, s)
	}
	e = rows.Err()
	resource.Close(rows)
	if e != nil {
		return nil, e
	}
	for i := range out {
		links, e := db.QueryContext(ctx, "SELECT target_id FROM ep_subscription_targets WHERE tenant_id=$1 AND id=$2 ORDER BY target_id", out[i].TenantID, out[i].ID)
		if e != nil {
			return nil, e
		}
		out[i].TargetIDs = []string{}
		for links.Next() {
			var id string
			if e = links.Scan(&id); e != nil {
				resource.Close(links)
				return nil, e
			}
			out[i].TargetIDs = append(out[i].TargetIDs, id)
		}
		e = links.Err()
		resource.Close(links)
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

func (c Control) Subscriptions(ctx context.Context, p domain.Principal, tenant string) ([]domain.Subscription, error) {
	if e := authorize(ctx, c.DB.SQL, p, tenant, "read"); e != nil {
		return nil, e
	}
	return subscriptions(ctx, c.DB.SQL, tenant, 0)
}

// RoomSubscriptions is an internal control-plane contract; callers must not expose its result directly.
func (c Control) RoomSubscriptions(ctx context.Context, room int64) ([]domain.Subscription, error) {
	return subscriptions(ctx, c.DB.SQL, "", room)
}

func (c Control) SaveSubscription(ctx context.Context, p domain.Principal, tenant string, s domain.Subscription, create bool) (domain.Subscription, error) {
	if e := s.Validate(); e != nil {
		return s, e
	}
	s.TenantID = tenant
	if create {
		s.ID = monitor.ID()
		s.Version = 1
	}
	policy, e := json.Marshal(s.Policy)
	if e != nil {
		return s, e
	}
	e = transaction(ctx, c.DB.SQL, func(tx *sql.Tx) error {
		if e := authorize(ctx, tx, p, tenant, "manage"); e != nil {
			return e
		}
		limit, e := lockTenant(ctx, tx, tenant)
		if e != nil {
			return e
		}
		if create {
			var count int
			if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM ep_subscriptions WHERE tenant_id=$1", tenant).Scan(&count); e != nil {
				return e
			}
			if count >= limit.MaxSubscriptions {
				return domain.ErrQuota
			}
			e = tx.QueryRowContext(ctx, "INSERT INTO ep_subscriptions(tenant_id,id,room,enabled,policy) VALUES($1,$2,$3,$4,$5) RETURNING created_at", tenant, s.ID, s.RoomID, s.Enabled, policy).Scan(&s.CreatedAt)
		} else {
			e = affected(tx.ExecContext(ctx, "UPDATE ep_subscriptions SET enabled=$3,policy=$4,version=version+1 WHERE tenant_id=$1 AND id=$2 AND version=$5 AND room=$6", tenant, s.ID, s.Enabled, policy, s.Version, s.RoomID))
			if e == nil {
				s.Version++
			}
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "DELETE FROM ep_subscription_targets WHERE tenant_id=$1 AND id=$2", tenant, s.ID); e != nil {
				return e
			}
		}
		if e != nil {
			return e
		}
		for _, id := range s.TargetIDs {
			if _, e = tx.ExecContext(ctx, "INSERT INTO ep_subscription_targets VALUES($1,$2,$3)", tenant, s.ID, id); e != nil {
				return e
			}
		}
		if !s.Enabled {
			if _, e = tx.ExecContext(ctx, "UPDATE ep_jobs SET state='disabled' WHERE tenant_id=$1 AND subscription_id=$2 AND state='pending'", tenant, s.ID); e != nil {
				return e
			}
		}
		return audit(ctx, tx, p, tenant, "subscription.save", s.ID)
	})
	return s, e
}

func (c Control) RotateCredentials(ctx context.Context, p domain.Principal, tenant string) error {
	return transaction(ctx, c.DB.SQL, func(tx *sql.Tx) error {
		if e := authorize(ctx, tx, p, tenant, "manage"); e != nil {
			return e
		}
		if _, e := lockTenant(ctx, tx, tenant); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT id,credentials FROM ep_targets WHERE tenant_id=$1 FOR UPDATE", tenant)
		if e != nil {
			return e
		}
		type item struct{ id, sealed string }
		items := []item{}
		for rows.Next() {
			var x item
			if e = rows.Scan(&x.id, &x.sealed); e != nil {
				resource.Close(rows)
				return e
			}
			items = append(items, x)
		}
		e = rows.Err()
		resource.Close(rows)
		if e != nil {
			return e
		}
		for _, x := range items {
			creds, e := c.DB.Vault.Open(tenant+":"+x.id, x.sealed)
			if e != nil {
				return e
			}
			encrypted, e := c.DB.Vault.Seal(tenant+":"+x.id, creds)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE ep_targets SET credentials=$3,version=version+1 WHERE tenant_id=$1 AND id=$2", tenant, x.id, encrypted); e != nil {
				return e
			}
		}
		return audit(ctx, tx, p, tenant, "credentials.rotate", tenant)
	})
}
