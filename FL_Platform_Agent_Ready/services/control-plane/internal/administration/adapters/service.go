package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/credentials"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

// Service is the session-bound HTTP entry point. Actor-only Store methods are
// retained only for noncredential internal compatibility. Password writes must
// use MutateCredential with the shared services and a server-resolved client.
type Service struct {
	Pool   *pgxpool.Pool
	Limits *credentials.Store
	Work   *credentials.WorkPool
	// Internal test seams, never populated by HTTP/configuration.
	verifyPassword func(string, string) bool
	passwordHash   func(string) string
}

func sessionFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, identity.ErrUnauthorized) {
		return Failure{401, "UNAUTHORIZED"}
	}
	var f Failure
	var org organization.Failure
	var credential credentials.Failure
	var pg *pgconn.PgError
	if errors.As(err, &f) || errors.As(err, &org) || errors.As(err, &credential) || errors.As(err, &pg) && pg.Code == "23505" {
		return err
	}
	return Failure{503, "DEPENDENCY_UNAVAILABLE"}
}

func (s Service) withSession(ctx context.Context, principal identity.Principal, change bool, fn func(context.Context, pgx.Tx, identity.User) error) error {
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	defer tx.Rollback(ctx)
	if change {
		err = authorization.Write(ctx, tx)
	} else {
		err = authorization.Read(ctx, tx)
	}
	if err != nil {
		return sessionFailure(err)
	}
	user, err := identity.Current(ctx, tx, principal)
	if err != nil {
		return sessionFailure(err)
	}
	if err = fn(ctx, tx, user); err != nil {
		return sessionFailure(err)
	}
	return sessionFailure(tx.Commit(ctx))
}

func (s Service) Mutate(ctx context.Context, principal identity.Principal, op, target, key, fingerprint string, in Input) (json.RawMessage, error) {
	var output json.RawMessage
	err := s.withSession(ctx, principal, true, func(ctx context.Context, tx pgx.Tx, user identity.User) error {
		if err := Validate(op, in); err != nil {
			return err
		}
		if !regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`).MatchString(key) {
			return Failure{400, "INVALID_REQUEST"}
		}
		if credentialOperation(op, in) {
			return Failure{503, "DEPENDENCY_UNAVAILABLE"}
		}
		return mutateTx(ctx, tx, user.ID, op, target, key, fingerprint, in, &output)
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (s Service) Page(ctx context.Context, principal identity.Principal, kind string, offset, limit int) ([]json.RawMessage, bool, error) {
	var items []json.RawMessage
	var more bool
	err := s.withSession(ctx, principal, false, func(ctx context.Context, tx pgx.Tx, user identity.User) error {
		if offset < 0 || limit < 1 || limit > 100 {
			return Failure{400, "INVALID_REQUEST"}
		}
		var err error
		items, more, err = pageTx(ctx, tx, user.ID, kind, offset, limit)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return items, more, nil
}
