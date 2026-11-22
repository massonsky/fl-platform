package organization

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// CheckIdentityRemoval must run under authorization.Write, before changing an
// enabled admin/researcher into a disabled user or global viewer. Personal and
// suspended organizations retain legacy account management semantics. A future
// shared-organization reactivation must require an effective owner of its own.
func CheckIdentityRemoval(ctx context.Context, tx pgx.Tx, userID string) error {
	var organizations, memberships bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.organizations') IS NOT NULL,
 to_regclass('public.organization_memberships') IS NOT NULL`).Scan(&organizations, &memberships); err != nil {
		return err
	}
	if !organizations && !memberships {
		return nil // Schema6 is still a supported private-workspace deployment.
	}
	if !organizations || !memberships {
		return errors.New("incomplete organization schema")
	}
	rows, err := tx.Query(ctx, `SELECT o.id FROM organizations o
 JOIN organization_memberships m ON m.organization_id=o.id
 WHERE o.kind='shared' AND o.state='active' AND m.user_id=$1
 AND m.role='owner' AND m.status='active' ORDER BY o.id FOR UPDATE OF o`, userID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		var replacement bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_memberships m
 JOIN users u ON u.id=m.user_id WHERE m.organization_id=$1 AND m.user_id<>$2
 AND m.role='owner' AND m.status='active' AND u.disabled_at IS NULL
 AND u.role IN ('admin','researcher'))`, id, userID).Scan(&replacement); err != nil {
			return err
		}
		if !replacement {
			return failure(409, "LAST_OWNER")
		}
	}
	return nil
}
