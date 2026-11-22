package tenantagent

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func (s Service) Create(ctx context.Context, p identity.Principal, org, id, name string) (Agent, error) {
	return serviceCall(ctx, s, p, true, func(ctx context.Context, tx pgx.Tx, user identity.User) (Agent, error) {
		name, err := normalizedName(name)
		if err != nil || !validID(id) {
			return Agent{}, invalid()
		}
		if err = managerScope(ctx, tx, user, org, true); err != nil {
			return Agent{}, err
		}
		return createTx(ctx, tx, user.ID, org, id, name)
	})
}

func createTx(ctx context.Context, tx pgx.Tx, actor, org, id, name string) (Agent, error) {
	existing, err := agentByID(ctx, tx, id)
	if err == nil {
		if existing.OrganizationID != org {
			return Agent{}, notFound()
		}
		if existing.Name != name {
			return Agent{}, conflict()
		}
		return existing, nil // Exact UUID replay returns current state, never resets it.
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, err
	}
	var slot int
	err = tx.QueryRow(ctx, `SELECT coalesce(min(slot),0)::integer FROM generate_series(1,$2::integer) slot
 WHERE NOT EXISTS(SELECT 1 FROM tenant_agents a WHERE a.organization_id=$1 AND a.registry_slot=slot)`, org, IdentityLimit).Scan(&slot)
	if err != nil {
		return Agent{}, err
	}
	if slot == 0 {
		return Agent{}, Failure{409, "AGENT_LIMIT", 0}
	}
	a, err := scanAgent(tx.QueryRow(ctx, `INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by)
 VALUES($1,$2,$3,$4,$5) RETURNING `+agentColumns, id, org, slot, name, actor))
	if err != nil {
		return Agent{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_events(organization_id,agent_id,actor_kind,actor_user_id,action,agent_revision,after_state)
 VALUES($1,$2,'user',$3,'agent_created',$4,$5)`, org, id, actor, a.Revision, a.State)
	return a, err
}

func (s Service) List(ctx context.Context, p identity.Principal, org string) ([]Agent, error) {
	return serviceCall(ctx, s, p, false, func(ctx context.Context, tx pgx.Tx, user identity.User) ([]Agent, error) {
		if err := managerScope(ctx, tx, user, org, false); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, "SELECT "+agentColumns+" FROM tenant_agents WHERE organization_id=$1 ORDER BY created_at,id LIMIT $2", org, IdentityLimit+1)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := make([]Agent, 0)
		for rows.Next() {
			a, err := scanAgent(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, a)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(items) > IdentityLimit {
			return nil, unavailable()
		}
		return items, nil
	})
}

func (s Service) Get(ctx context.Context, p identity.Principal, org, id string) (Details, error) {
	return serviceCall(ctx, s, p, false, func(ctx context.Context, tx pgx.Tx, user identity.User) (Details, error) {
		if !validID(id) {
			return Details{}, invalid()
		}
		if err := managerScope(ctx, tx, user, org, false); err != nil {
			return Details{}, err
		}
		return detailsTx(ctx, tx, org, id)
	})
}
