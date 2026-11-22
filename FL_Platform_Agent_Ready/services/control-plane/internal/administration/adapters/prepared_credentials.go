package adapters

import (
	"context"
	"encoding/json"
	"net/netip"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

// CredentialMutation is internal application input, not a JSON request. The
// transport supplies Client from its reviewed resolver and Fingerprint from the
// original canonical request. Callers cannot supply a prepared password hash.
type CredentialMutation struct {
	Client                              netip.Addr
	Operation, Target, Key, Fingerprint string
	Input                               Input
}

type preparedCredential struct {
	snapshot credentialSnapshot
	hash     string
}

func credentialOperation(op string, in Input) bool {
	return op == "user.create" || op == "password.change" || op == "user.update" && in.Password != ""
}

func validateCredential(command CredentialMutation) error {
	if !credentialOperation(command.Operation, command.Input) ||
		!regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`).MatchString(command.Key) {
		return Failure{400, "INVALID_REQUEST"}
	}
	if err := Validate(command.Operation, command.Input); err != nil {
		return err
	}
	if command.Operation == "user.update" {
		id, err := uuid.Parse(command.Target)
		if err != nil || id == uuid.Nil {
			return Failure{400, "INVALID_REQUEST"}
		}
	} else if command.Target != "" {
		return Failure{400, "INVALID_REQUEST"}
	}
	return nil
}

// MutateCredential has one original ten-second deadline. Short preflight and
// final transactions surround synchronous credential work; neither transaction
// nor authorization guard survives across Verify/Hash. HTTP cutover is separate.
func (s Service) MutateCredential(ctx context.Context, principal identity.Principal, command CredentialMutation) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	if s.Pool == nil {
		return nil, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	// Copy pointer-valued input before any asynchronous/interleaved operation.
	if command.Input.Disabled != nil {
		value := *command.Input.Disabled
		command.Input.Disabled = &value
	}
	var prepared preparedCredential
	var replay json.RawMessage
	err := s.withSession(ctx, principal, false, func(ctx context.Context, tx pgx.Tx, user identity.User) error {
		if command.Operation != "password.change" && user.Role != "admin" {
			return Failure{403, "FORBIDDEN"}
		}
		if err := validateCredential(command); err != nil {
			return err
		}
		var err error
		prepared.snapshot, replay, err = snapshotCredential(ctx, tx, user.ID, command)
		return err
	})
	if err != nil {
		return nil, err
	}
	// A currently authorized persisted replay requires no new work or budget.
	if replay != nil {
		return replay, nil
	}
	if s.Limits == nil || s.Work == nil {
		return nil, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	units := 1
	if command.Operation == "password.change" {
		units = 2
	}
	if err = s.Limits.Client(ctx, command.Client, units); err != nil {
		return nil, sessionFailure(err)
	}
	verify, hash := s.verifyPassword, s.passwordHash
	if verify == nil {
		verify = identity.Verify
	}
	if hash == nil {
		hash = identity.PasswordHash
	}
	err = s.Work.Do(ctx,
		func(ctx context.Context) error { return s.Limits.Actor(ctx, prepared.snapshot.actor, units) },
		func(context.Context) error {
			if command.Operation == "password.change" && !verify(command.Input.CurrentPassword, prepared.snapshot.password.String) {
				return Failure{403, "CURRENT_PASSWORD_INVALID"}
			}
			return nil
		},
		func(context.Context) error { prepared.hash = hash(command.Input.Password); return nil },
	)
	if err != nil {
		return nil, sessionFailure(err)
	}
	var output json.RawMessage
	err = s.withSession(ctx, principal, true, func(ctx context.Context, tx pgx.Tx, user identity.User) error {
		return mutateWithPreparedTx(ctx, tx, user.ID, command.Operation, command.Target,
			command.Key, command.Fingerprint, command.Input, &prepared, &output)
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}
