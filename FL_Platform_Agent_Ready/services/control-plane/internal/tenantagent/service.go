package tenantagent

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

// Service is a human, session-bound metadata boundary. It neither authenticates
// machines nor admits these identities to legacy or organization training.
type Service struct{ Pool *pgxpool.Pool }

func serviceCall[T any](ctx context.Context, s Service, p identity.Principal, change bool, fn func(context.Context, pgx.Tx, identity.User) (T, error)) (T, error) {
	var zero T
	if s.Pool == nil {
		return zero, unavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return zero, unavailable()
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if change {
		err = authorization.Write(ctx, tx)
	} else {
		err = authorization.Read(ctx, tx)
	}
	if err != nil {
		return zero, serviceFailure(err)
	}
	user, err := identity.Current(ctx, tx, p)
	if err != nil {
		return zero, serviceFailure(err)
	}
	if err = schemaReady(ctx, tx); err != nil {
		return zero, serviceFailure(err)
	}
	// The callback must use this bounded context, including every later query.
	// Initial Current after guard is the operation's authorization boundary;
	// expiry may occur during its bounded duration, as in other human services.
	value, err := fn(ctx, tx, user)
	if err != nil {
		return zero, serviceFailure(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, serviceFailure(err)
	}
	return value, nil
}

func serviceFailure(err error) error {
	if errors.Is(err, identity.ErrUnauthorized) {
		return unauthorized()
	}
	var f Failure
	if errors.As(err, &f) {
		return f
	}
	return unavailable()
}
