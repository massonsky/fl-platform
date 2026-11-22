package projectaccess

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

// Service is the session-bound entry point. Transport must still validate its
// explicit organization routes and signed cursors before invoking this service.
type Service struct{ Pool *pgxpool.Pool }

func serviceFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, identity.ErrUnauthorized) {
		return failure(401, "UNAUTHORIZED")
	}
	var f Failure
	if errors.As(err, &f) {
		return err
	}
	return failure(503, "DEPENDENCY_UNAVAILABLE")
}

func serviceSchema(ctx context.Context, tx pgx.Tx) error {
	var complete bool
	err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM pg_attribute WHERE attrelid='public.projects'::regclass
 AND attname IN ('created_by','access_mode','visibility','acl_revision','organization_id') AND NOT attisdropped)=5
 AND to_regclass('public.project_members') IS NOT NULL
 AND to_regclass('public.project_access_events') IS NOT NULL
 AND to_regclass('public.organizations') IS NOT NULL
 AND to_regclass('public.organization_memberships') IS NOT NULL`).Scan(&complete)
	if err != nil {
		return err
	}
	if !complete {
		return failure(503, "DEPENDENCY_UNAVAILABLE")
	}
	return nil
}

func serviceCall[T any](ctx context.Context, s Service, principal identity.Principal, change bool, fn func(context.Context, pgx.Tx, identity.User) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	var zero T
	tx, err := (Store{Pool: s.Pool}).begin(ctx, change)
	if err != nil {
		return zero, serviceFailure(err)
	}
	defer tx.Rollback(ctx)
	user, err := identity.Current(ctx, tx, principal)
	if err != nil {
		return zero, serviceFailure(err)
	}
	if err = serviceSchema(ctx, tx); err != nil {
		return zero, serviceFailure(err)
	}
	value, err := fn(ctx, tx, user)
	if err != nil {
		return zero, serviceFailure(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, serviceFailure(err)
	}
	return value, nil
}

func (s Service) Create(ctx context.Context, principal identity.Principal, org, id string, in CreateInput) (Project, error) {
	return serviceCall(ctx, s, principal, true, func(ctx context.Context, tx pgx.Tx, user identity.User) (Project, error) {
		return createTx(ctx, tx, user.ID, org, id, in)
	})
}

func (s Service) Get(ctx context.Context, principal identity.Principal, org, id string) (Project, error) {
	return serviceCall(ctx, s, principal, false, func(ctx context.Context, tx pgx.Tx, user identity.User) (Project, error) {
		if !validIDs(org, id) {
			return Project{}, failure(400, "INVALID_REQUEST")
		}
		return Check(ctx, tx, user.ID, ResourceRef{Kind: "project", ID: id, OrganizationID: org}, Read)
	})
}

func (s Service) SaveGrant(ctx context.Context, principal identity.Principal, org, project, target string, in GrantInput) (Grant, error) {
	return serviceCall(ctx, s, principal, true, func(ctx context.Context, tx pgx.Tx, user identity.User) (Grant, error) {
		return saveGrantTx(ctx, tx, user.ID, org, project, target, in)
	})
}

func (s Service) SetVisibility(ctx context.Context, principal identity.Principal, org, project string, in VisibilityInput) (Project, error) {
	return serviceCall(ctx, s, principal, true, func(ctx context.Context, tx pgx.Tx, user identity.User) (Project, error) {
		return setVisibilityTx(ctx, tx, user.ID, org, project, in)
	})
}
