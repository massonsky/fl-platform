package organization

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

// This is an anti-abuse guard for the foundation, not a commercial entitlement.
const MaxCreatedSharedOrganizations = 20

const scopeColumns = `o.id,o.name,o.kind,o.state,o.revision,o.created_at,m.role,m.status,u.role`
const scopeJoin = ` FROM organizations o JOIN organization_memberships m ON m.organization_id=o.id
 JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.status='active' AND u.disabled_at IS NULL`

func scanOrganization(row pgx.Row) (Organization, error) {
	var o Organization
	var globalRole string
	err := row.Scan(&o.ID, &o.Name, &o.Kind, &o.State, &o.Revision, &o.CreatedAt, &o.Role, &o.MembershipStatus, &globalRole)
	o.CanManageMembers = PermissionsFor(globalRole, false, o.Role, o.MembershipStatus, o.State).ManageMembers
	return o, err
}

// Store methods are actor-only foundation entry points for trusted internal
// callers. HTTP must use Service and a session-bound Principal instead.
func (s Store) List(ctx context.Context, actorID string) ([]Organization, error) {
	if !validIDs(actorID) {
		return nil, failure(400, "INVALID_REQUEST")
	}
	return organizationTransaction(ctx, s, false, func(ctx context.Context, tx pgx.Tx) ([]Organization, error) {
		return listTx(ctx, tx, actorID)
	})
}
func listTx(ctx context.Context, tx pgx.Tx, actorID string) ([]Organization, error) {
	if !validIDs(actorID) {
		return nil, failure(400, "INVALID_REQUEST")
	}
	rows, err := tx.Query(ctx, "SELECT "+scopeColumns+scopeJoin+" ORDER BY o.created_at,o.id", actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Organization{}
	for rows.Next() {
		o, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, o)
	}
	return items, rows.Err()
}

// The lock keeps legacy role/disabled state stable until this write commits.
func lockActor(ctx context.Context, tx pgx.Tx, actorID string, exclusive bool) (string, error) {
	lock := " FOR SHARE"
	if exclusive {
		lock = " FOR UPDATE"
	}
	var role string
	var disabled bool
	err := tx.QueryRow(ctx, "SELECT role,disabled_at IS NOT NULL FROM users WHERE id=$1"+lock, actorID).Scan(&role, &disabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && disabled) {
		return "", failure(403, "FORBIDDEN")
	}
	return role, err
}

func lockScope(ctx context.Context, tx pgx.Tx, actorID, organizationID string) (Organization, error) {
	if _, err := lockActor(ctx, tx, actorID, false); err != nil {
		return Organization{}, err
	}
	// Lock organization before any membership row to avoid opposing owner edits.
	var locked string
	err := tx.QueryRow(ctx, "SELECT id FROM organizations WHERE id=$1 FOR UPDATE", organizationID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Organization{}, failure(404, "NOT_FOUND")
	}
	if err != nil {
		return Organization{}, err
	}
	o, err := scanOrganization(tx.QueryRow(ctx, "SELECT "+scopeColumns+scopeJoin+
		" AND o.id=$2 FOR UPDATE OF m", actorID, organizationID))
	if errors.Is(err, pgx.ErrNoRows) {
		err = failure(404, "NOT_FOUND")
	}
	return o, err
}

// Create replay returns current state without recreating/promoting membership.
func (s Store) Create(ctx context.Context, actorID, id, name string) (Organization, error) {
	if !validIDs(actorID, id) {
		return Organization{}, failure(400, "INVALID_REQUEST")
	}
	var err error
	if name, err = normalizeName(name); err != nil {
		return Organization{}, err
	}
	return organizationTransaction(ctx, s, true, func(ctx context.Context, tx pgx.Tx) (Organization, error) {
		return createTx(ctx, tx, actorID, id, name)
	})
}
