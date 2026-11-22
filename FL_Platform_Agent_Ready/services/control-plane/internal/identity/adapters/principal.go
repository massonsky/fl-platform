package adapters

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Principal binds an internal operation to one authenticated session. Neither
// field is a public DTO, and a user ID alone never authorizes protected work.
type Principal struct {
	UserID      string `json:"-"`
	SessionHash string `json:"-"`
}

func (u User) Principal() Principal { return Principal{UserID: u.ID, SessionHash: u.SessionHash} }

// Current requires authorization.Read/Write before any row locks in the same
// transaction. Use wall time: a transaction can wait for a guard past expiry.
func Current(ctx context.Context, tx pgx.Tx, p Principal) (User, error) {
	var user User
	id, err := uuid.Parse(p.UserID)
	if err != nil || id == uuid.Nil {
		return user, ErrUnauthorized
	}
	hash, err := hex.DecodeString(p.SessionHash)
	if err != nil || len(hash) != 32 {
		return user, ErrUnauthorized
	}
	err = tx.QueryRow(ctx, `SELECT u.id::text,u.display_name,u.role FROM users u
 JOIN sessions s ON s.user_id=u.id WHERE u.id=$1 AND s.token_hash=$2
 AND s.expires_at>clock_timestamp() AND u.disabled_at IS NULL`, p.UserID, p.SessionHash).Scan(&user.ID, &user.Name, &user.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return user, ErrUnauthorized
	}
	if err == nil {
		user.SessionHash = p.SessionHash
	}
	return user, err
}
