package projectaccess

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

// Foundation only: must not be routed until legacy APIs exclude organization rows.
type Store struct{ Pool *pgxpool.Pool }

func (s Store) begin(ctx context.Context, change bool) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if change {
		err = authorization.Write(ctx, tx)
	} else {
		err = authorization.Read(ctx, tx)
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func identityRole(ctx context.Context, tx pgx.Tx, actor string) (string, error) {
	var role string
	var disabled bool
	err := tx.QueryRow(ctx, "SELECT role,disabled_at IS NOT NULL FROM users WHERE id=$1 FOR SHARE", actor).Scan(&role, &disabled)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && disabled {
		return "", failure(403, "FORBIDDEN")
	}
	return role, err
}
func organizationPolicy(ctx context.Context, tx pgx.Tx, actor, org string) (Policy, error) {
	p := Policy{Mode: "organization"}
	var err error
	p.GlobalRole, err = identityRole(ctx, tx, actor)
	if err != nil {
		return p, err
	}
	err = tx.QueryRow(ctx, `SELECT o.kind,o.state,m.role,m.status FROM organizations o
 JOIN organization_memberships m ON m.organization_id=o.id WHERE o.id=$1 AND m.user_id=$2
 AND m.status='active' FOR SHARE OF o,m`, org, actor).Scan(&p.OrganizationKind, &p.OrganizationState, &p.OrganizationRole, &p.MembershipStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, failure(404, "NOT_FOUND")
	}
	return p, err
}
func sameID(a, b string) bool {
	x, e1 := uuid.Parse(a)
	y, e2 := uuid.Parse(b)
	return e1 == nil && e2 == nil && x == y
}
func event(ctx context.Context, tx pgx.Tx, p Project, actor, target, action string, before, after any) error {
	b, err := json.Marshal(before)
	if err != nil {
		return err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return err
	}
	var targetID any
	if target != "" {
		targetID = target
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_access_events(project_id,organization_id,actor_id,target_user_id,action,before_json,after_json)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ID, p.OrganizationID, actor, targetID, action, b, a)
	return err
}

func (s Store) Create(ctx context.Context, actor, org, id string, in CreateInput) (Project, error) {
	tx, err := s.begin(ctx, true)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	output, err := createTx(ctx, tx, actor, org, id, in)
	if err != nil {
		return Project{}, err
	}
	return output, tx.Commit(ctx)
}

func createTx(ctx context.Context, tx pgx.Tx, actor, org, id string, in CreateInput) (Project, error) {
	var output Project
	if !validIDs(actor, org, id) {
		return output, failure(400, "INVALID_REQUEST")
	}
	in, err := validateCreate(in)
	if err != nil {
		return output, err
	}
	p, err := organizationPolicy(ctx, tx, actor, org)
	if err != nil {
		return output, err
	}
	if p.OrganizationKind != "shared" || p.OrganizationState != "active" || p.GlobalRole == "viewer" || (p.OrganizationRole != "owner" && p.OrganizationRole != "admin") {
		return output, failure(403, "FORBIDDEN")
	}
	var creator, organization, mode, name, slug string
	err = tx.QueryRow(ctx, `SELECT coalesce(created_by,owner_id),coalesce(organization_id::text,''),access_mode,name,slug
 FROM projects WHERE id=$1 FOR UPDATE`, id).Scan(&creator, &organization, &mode, &name, &slug)
	if err == nil {
		if mode != "organization" || !sameID(creator, actor) || !sameID(organization, org) {
			return output, failure(404, "NOT_FOUND")
		}
		if name != in.Name || slug != in.Slug {
			return output, failure(409, "VERSION_CONFLICT")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return output, err
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO projects(id,owner_id,created_by,organization_id,access_mode,slug,name)
 VALUES($1,$2,$2,$3,'organization',$4,$5)`, id, actor, org, in.Slug, in.Name)
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "projects_organization_slug_key" {
				return output, failure(409, "SLUG_CONFLICT")
			}
			return output, err
		}
		if err = event(ctx, tx, Project{ID: id, OrganizationID: org}, actor, "", "project_created", map[string]any{}, map[string]any{"visibility": "private", "acl_revision": 1}); err != nil {
			return output, err
		}
	}
	return loadProject(ctx, tx, actor, ResourceRef{Kind: "project", ID: id, OrganizationID: org})
}
