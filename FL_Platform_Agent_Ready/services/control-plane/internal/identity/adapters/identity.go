package adapters

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/credentials"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	"golang.org/x/crypto/argon2"
	"strings"
)

type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	SessionHash string `json:"-"`
}
type Store struct {
	Pool   *pgxpool.Pool
	Limits *credentials.Store
	Work   *credentials.WorkPool
	// Internal test seam; API/config never replaces the production Verify function.
	verifyPassword func(string, string) bool
}

// ErrUnauthorized means credentials were checked and rejected, not a DB outage.
var ErrUnauthorized = errors.New("UNAUTHORIZED")

func (s Store) Logout(ctx context.Context, token string) error {
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	return db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := authorization.Write(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", Hash(token))
		return err
	})
}

func Token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func PasswordHash(password string) string {
	salt := Token()[:32]
	hash := argon2.IDKey([]byte(password), []byte(salt), 3, 64*1024, 2, 32)
	return "argon2id-v1$" + salt + "$" + hex.EncodeToString(hash)
}
func Verify(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id-v1" {
		return false
	}
	expected, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), []byte(parts[1]), 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(expected, actual) == 1
}
func (s Store) Authenticate(ctx context.Context, token string) (User, error) {
	var user User
	err := s.Pool.QueryRow(ctx, `SELECT u.id::text,u.display_name,u.role FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.disabled_at IS NULL`, Hash(token)).Scan(&user.ID, &user.Name, &user.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return user, ErrUnauthorized
	}
	if err == nil {
		user.SessionHash = Hash(token)
	}
	return user, err
}
