// Package credentials contains shared budgets; callers must debit Client before
// Account/Actor. It neither authenticates users nor replaces the work semaphore.
package credentials

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"net/netip"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	policyVersion    = "credential-v1:client20/10;account5/5;actor10/3;cap16384;ret1800;gc500/60"
	bucketCap        = 16384
	retention        = 30 * time.Minute
	operationTimeout = 2 * time.Second
	gcBatch          = 500
	gcInterval       = time.Minute
)

type Failure struct {
	Status     int
	Code       string
	RetryAfter int
}

func (e Failure) Error() string { return e.Code }

func unavailable() error { return Failure{503, "DEPENDENCY_UNAVAILABLE", 3} }
func invalid() error     { return Failure{400, "INVALID_REQUEST", 0} }

type policy struct {
	kind     string
	interval time.Duration
	burst    int
}

var policies = [...]policy{{"client", 3 * time.Second, 10}, {"account", 12 * time.Second, 5}, {"actor", 6 * time.Second, 3}}

// Store is safe for concurrent use. A new instance does not reset PostgreSQL state.
type Store struct {
	pool        *pgxpool.Pool
	key         [32]byte
	fingerprint []byte
}

// New accepts decoded random key bytes, never a session/cursor key derivation.
// File encoding and provisioning belong to the API wiring, not to this package.
func New(pool *pgxpool.Pool, key []byte) (*Store, error) {
	if pool == nil || len(key) != 32 {
		return nil, unavailable()
	}
	s := &Store{pool: pool}
	copy(s.key[:], key)
	s.fingerprint = s.digest("policy", policyVersion)
	return s, nil
}

func (s *Store) digest(kind, value string) []byte {
	h := hmac.New(sha256.New, s.key[:])
	_, _ = h.Write([]byte("fl/credential/v1\x00" + kind + "\x00" + value))
	return h.Sum(nil)
}

// Client groups IPv6 by /64; users behind NAT share a budget. Headers are not input.
func (s *Store) Client(ctx context.Context, address netip.Addr, units int) error {
	if !address.IsValid() || address.Zone() != "" {
		return invalid()
	}
	address = address.Unmap()
	bits := 64
	if address.Is4() {
		bits = 32
	}
	return s.consume(ctx, policies[0], netip.PrefixFrom(address, bits).Masked().String(), units)
}

// Account preserves exact case-sensitive lookup bytes and always costs one unit.
func (s *Store) Account(ctx context.Context, username string) error {
	if username == "" || len(username) > 64 || !utf8.ValidString(username) {
		return invalid()
	}
	for _, c := range username {
		if unicode.IsControl(c) {
			return invalid()
		}
	}
	return s.consume(ctx, policies[1], username, 1)
}

// Actor must be the identity returned by Current, never an ID from request JSON.
func (s *Store) Actor(ctx context.Context, id string, units int) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil {
		return invalid()
	}
	return s.consume(ctx, policies[2], parsed.String(), units)
}
