package organization

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

const membershipColumns = `m.user_id,u.display_name,m.role,m.status,m.revision,m.updated_at`

func scanMembership(row pgx.Row) (Membership, error) {
	var m Membership
	err := row.Scan(&m.UserID, &m.DisplayName, &m.Role, &m.Status, &m.Revision, &m.UpdatedAt)
	return m, err
}

func (s Store) ListMembers(ctx context.Context, actorID, organizationID string) ([]Membership, error) {
	if !validIDs(actorID, organizationID) {
		return nil, failure(400, "INVALID_REQUEST")
	}
	return organizationTransaction(ctx, s, false, func(ctx context.Context, tx pgx.Tx) ([]Membership, error) {
		return listMembersTx(ctx, tx, actorID, organizationID)
	})
}

func listMembersTx(ctx context.Context, tx pgx.Tx, actorID, organizationID string) ([]Membership, error) {
	if !validIDs(actorID, organizationID) {
		return nil, failure(400, "INVALID_REQUEST")
	}
	o, err := lockScope(ctx, tx, actorID, organizationID)
	if err != nil {
		return nil, err
	}
	if o.Role != "owner" && o.Role != "admin" {
		return nil, failure(403, "FORBIDDEN")
	}
	rows, err := tx.Query(ctx, "SELECT "+membershipColumns+` FROM organization_memberships m JOIN users u ON u.id=m.user_id
 WHERE m.organization_id=$1 ORDER BY u.display_name,m.user_id`, organizationID)
	if err != nil {
		return nil, err
	}
	items := []Membership{}
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, m)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s Store) ChangeMember(ctx context.Context, actorID, organizationID, userID string, in MemberChange) (Membership, error) {
	if !validIDs(actorID, organizationID, userID) || !validChange(in) {
		return Membership{}, failure(400, "INVALID_REQUEST")
	}
	return organizationTransaction(ctx, s, true, func(ctx context.Context, tx pgx.Tx) (Membership, error) {
		return changeMemberTx(ctx, tx, actorID, organizationID, userID, in)
	})
}

func changeMemberTx(ctx context.Context, tx pgx.Tx, actorID, organizationID, userID string, in MemberChange) (Membership, error) {
	var m Membership
	if !validIDs(actorID, organizationID, userID) || !validChange(in) {
		return m, failure(400, "INVALID_REQUEST")
	}
	o, err := lockScope(ctx, tx, actorID, organizationID)
	if err != nil {
		return m, err
	}
	if !o.CanManageMembers {
		return m, failure(403, "FORBIDDEN")
	}
	m, err = scanMembership(tx.QueryRow(ctx, "SELECT "+membershipColumns+` FROM organization_memberships m JOIN users u ON u.id=m.user_id
 WHERE m.organization_id=$1 AND m.user_id=$2 FOR UPDATE OF m FOR SHARE OF u`, organizationID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, failure(404, "NOT_FOUND")
	}
	if err != nil {
		return m, err
	}
	if !canChangeMember(o.Role, m.Role, in.Role) {
		return m, failure(403, "FORBIDDEN")
	}
	if m.Revision != in.Revision {
		return m, failure(409, "VERSION_CONFLICT")
	}
	if m.Role == in.Role && m.Status == in.Status {
		return m, nil
	}
	before := membershipAudit(m)
	updated, err := scanMembership(tx.QueryRow(ctx, `WITH changed AS (
 UPDATE organization_memberships SET role=$3,status=$4,revision=revision+1,updated_at=now()
 WHERE organization_id=$1 AND user_id=$2 RETURNING *)
 SELECT `+membershipColumns+" FROM changed m JOIN users u ON u.id=m.user_id", organizationID, userID, in.Role, in.Status))
	if err != nil {
		return m, err
	}
	// Lock the global identities as well: viewer/disabled owners do not satisfy the guard.
	rows, err := tx.Query(ctx, `SELECT u.id FROM organization_memberships m JOIN users u ON u.id=m.user_id
 WHERE m.organization_id=$1 AND m.role='owner' AND m.status='active'
 AND u.disabled_at IS NULL AND u.role IN ('admin','researcher') FOR SHARE OF u`, organizationID)
	if err != nil {
		return m, err
	}
	owners, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return m, err
	}
	if len(owners) == 0 {
		return m, failure(409, "LAST_OWNER")
	}
	if m.Status != "revoked" && in.Status == "revoked" {
		if err = revokeProjectAccess(ctx, tx, organizationID, actorID, userID); err != nil {
			return m, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO organization_events(organization_id,actor_id,target_user_id,action,before_json,after_json)
 VALUES($1,$2,$3,'membership_changed',$4,$5)`, organizationID, actorID, userID, before, membershipAudit(updated)); err != nil {
		return m, err
	}
	if _, err = tx.Exec(ctx, "UPDATE organizations SET revision=revision+1,updated_at=now() WHERE id=$1", organizationID); err != nil {
		return m, err
	}
	return updated, nil
}

func membershipAudit(m Membership) []byte {
	data, _ := json.Marshal(struct {
		Role     string `json:"role"`
		Status   string `json:"status"`
		Revision int    `json:"revision"`
	}{m.Role, m.Status, m.Revision})
	return data
}
