package tenantagent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func notFound() error  { return Failure{404, "NOT_FOUND", 0} }
func forbidden() error { return Failure{403, "FORBIDDEN", 0} }
func conflict() error  { return Failure{409, "VERSION_CONFLICT", 0} }

// Called only after the guard and Current in the same transaction. Membership
// presence is resolved before role/global-cap checks, including for platform
// administrators: knowing an organization UUID does not reveal its existence.
func managerScope(ctx context.Context, tx pgx.Tx, user identity.User, org string, change bool) error {
	if !validID(org) {
		return invalid()
	}
	query := `SELECT o.kind,o.state,m.role FROM organizations o
 JOIN organization_memberships m ON m.organization_id=o.id
 WHERE o.id=$1 AND m.user_id=$2 AND m.status='active'`
	if change {
		query += " FOR UPDATE OF o"
	}
	var kind, state, role string
	err := tx.QueryRow(ctx, query, org, user.ID).Scan(&kind, &state, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	if kind != "shared" || state != "active" || (role != "owner" && role != "admin") || (user.Role != "admin" && user.Role != "researcher") {
		return forbidden()
	}
	return nil
}
