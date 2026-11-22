package organization

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

// Service is the public session-bound boundary; actor-only Store is internal.
type Service struct{ Pool *pgxpool.Pool }

func organizationTransaction[T any](ctx context.Context, s Store, change bool, fn func(context.Context, pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	if change {
		err = authorization.Write(ctx, tx)
	} else {
		err = authorization.Read(ctx, tx)
	}
	if err != nil {
		return zero, err
	}
	result, err := fn(ctx, tx)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return result, nil
}

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
	var ready bool
	err := tx.QueryRow(ctx, `SELECT to_regclass('public.organizations') IS NOT NULL
 AND to_regclass('public.organization_memberships') IS NOT NULL
 AND to_regclass('public.organization_events') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return failure(503, "DEPENDENCY_UNAVAILABLE")
	}
	return nil
}

func serviceCall[T any](ctx context.Context, s Service, p identity.Principal, change bool, fn func(context.Context, pgx.Tx, identity.User) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	value, err := organizationTransaction(ctx, Store{Pool: s.Pool}, change, func(ctx context.Context, tx pgx.Tx) (T, error) {
		var zero T
		user, err := identity.Current(ctx, tx, p)
		if err != nil {
			return zero, err
		}
		if err = serviceSchema(ctx, tx); err != nil {
			return zero, err
		}
		// Current after guard is the same authorization boundary as C1/C3.
		// Security writers wait for commit; mid-operation expiry may occur within10s.
		return fn(ctx, tx, user)
	})
	return value, serviceFailure(err)
}

func (s Service) List(ctx context.Context, p identity.Principal) ([]Organization, error) {
	return serviceCall(ctx, s, p, false, func(ctx context.Context, tx pgx.Tx, user identity.User) ([]Organization, error) {
		return listTx(ctx, tx, user.ID)
	})
}
func (s Service) Create(ctx context.Context, p identity.Principal, id, name string) (Organization, error) {
	return serviceCall(ctx, s, p, true, func(ctx context.Context, tx pgx.Tx, user identity.User) (Organization, error) {
		return createTx(ctx, tx, user.ID, id, name)
	})
}
func (s Service) ListMembers(ctx context.Context, p identity.Principal, org string) ([]Membership, error) {
	return serviceCall(ctx, s, p, false, func(ctx context.Context, tx pgx.Tx, user identity.User) ([]Membership, error) {
		return listMembersTx(ctx, tx, user.ID, org)
	})
}
func (s Service) ChangeMember(ctx context.Context, p identity.Principal, org, userID string, in MemberChange) (Membership, error) {
	return serviceCall(ctx, s, p, true, func(ctx context.Context, tx pgx.Tx, user identity.User) (Membership, error) {
		return changeMemberTx(ctx, tx, user.ID, org, userID, in)
	})
}
