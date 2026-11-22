package adapters

import (
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/credentials"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

// LoginInput is an application input, not a JSON DTO. Client comes from the
// reviewed transport resolver; callers cannot supply a forwarding-header value.
type LoginInput struct {
	Client   netip.Addr
	Username string
	Password string
}

type loginSnapshot struct {
	id, hash string
	eligible bool
}

// A valid-format fixed v1 record makes rejected/absent identities run the same
// Argon parameters. Its comparison result never permits authentication.
const dummyPasswordHash = "argon2id-v1$00000000000000000000000000000000$0000000000000000000000000000000000000000000000000000000000000000"

func loginUnavailable() error {
	return credentials.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE", RetryAfter: 3}
}

func validLogin(input LoginInput) bool {
	if len(input.Username) < 1 || len(input.Username) > 64 || !utf8.ValidString(input.Username) ||
		len(input.Password) < 1 || len(input.Password) > 128 || !utf8.ValidString(input.Password) {
		return false
	}
	for _, r := range input.Username {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPasswordRecord(hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 3 || parts[0] != "argon2id-v1" || len(parts[1]) != 32 || len(parts[2]) != 64 {
		return false
	}
	_, saltErr := hex.DecodeString(parts[1])
	_, hashErr := hex.DecodeString(parts[2])
	return saltErr == nil && hashErr == nil
}

// Login shares a single five-second budget across limits, work and final commit.
// Authentication and logout intentionally do not depend on these new services.
func (s Store) Login(ctx context.Context, input LoginInput) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !validLogin(input) {
		return "", credentials.Failure{Status: 400, Code: "INVALID_REQUEST"}
	}
	if s.Pool == nil || s.Limits == nil || s.Work == nil {
		return "", loginUnavailable()
	}
	if err := s.Limits.Client(ctx, input.Client, 1); err != nil {
		return "", err
	}
	snapshot := loginSnapshot{}
	verify := s.verifyPassword
	if verify == nil {
		verify = Verify
	}
	err := s.Work.Do(ctx,
		func(ctx context.Context) error { return s.Limits.Account(ctx, input.Username) },
		func(ctx context.Context) error {
			var enabled bool
			err := s.Pool.QueryRow(ctx, "SELECT id::text,coalesce(password_hash,''),disabled_at IS NULL FROM users WHERE external_subject=$1", input.Username).Scan(&snapshot.id, &snapshot.hash, &enabled)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return loginUnavailable()
			}
			snapshot.eligible = err == nil && enabled && validPasswordRecord(snapshot.hash)
			if !snapshot.eligible {
				snapshot.hash = dummyPasswordHash
			}
			return nil
		},
		func(context.Context) error {
			matched := verify(input.Password, snapshot.hash)
			if !snapshot.eligible || !matched {
				return ErrUnauthorized
			}
			return nil
		},
	)
	if err != nil {
		var f credentials.Failure
		if errors.Is(err, ErrUnauthorized) || errors.As(err, &f) {
			return "", err
		}
		return "", loginUnavailable()
	}
	token := Token()
	err = db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		// No row/security lock was held while verifying. Take the read guard FIRST.
		if err := authorization.Read(ctx, tx); err != nil {
			return err
		}
		var hash string
		var enabled bool
		err := tx.QueryRow(ctx, "SELECT coalesce(password_hash,''),disabled_at IS NULL FROM users WHERE id=$1", snapshot.id).Scan(&hash, &enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if !enabled || hash != snapshot.hash {
			return ErrUnauthorized
		}
		_, err = tx.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '8 hours')", Hash(token), snapshot.id)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return "", ErrUnauthorized
		}
		return "", loginUnavailable()
	}
	return token, nil
}
