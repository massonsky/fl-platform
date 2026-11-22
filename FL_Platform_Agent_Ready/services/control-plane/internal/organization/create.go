package organization

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Caller owns the guarded transaction. No nested transaction or authorization
// outside that boundary is sufficient for invoking this helper from HTTP.
func createTx(ctx context.Context, tx pgx.Tx, actorID, id, name string) (Organization, error) {
	var o Organization
	var err error
	if !validIDs(actorID, id) {
		return o, failure(400, "INVALID_REQUEST")
	}
	if name, err = normalizeName(name); err != nil {
		return o, err
	}
	globalRole, err := lockActor(ctx, tx, actorID, true)
	if err != nil {
		return o, err
	}
	if globalRole != "admin" && globalRole != "researcher" {
		return o, failure(403, "FORBIDDEN")
	}
	var creator, currentName string
	err = tx.QueryRow(ctx, "SELECT created_by,name FROM organizations WHERE id=$1 FOR UPDATE", id).Scan(&creator, &currentName)
	if err == nil {
		if creator != actorID {
			return o, failure(404, "NOT_FOUND")
		}
		if currentName != name {
			return o, failure(409, "VERSION_CONFLICT")
		}
		return lockScope(ctx, tx, actorID, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return o, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM organizations WHERE created_by=$1 AND kind='shared'", actorID).Scan(&count); err != nil {
		return o, err
	}
	if count >= MaxCreatedSharedOrganizations {
		return o, failure(409, "ORGANIZATION_LIMIT")
	}
	var inserted string
	err = tx.QueryRow(ctx, `INSERT INTO organizations(id,name,kind,created_by) VALUES($1,$2,'shared',$3)
 ON CONFLICT(id) DO NOTHING RETURNING id`, id, name, actorID).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, failure(404, "NOT_FOUND")
	}
	if err != nil {
		return o, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", id, actorID); err != nil {
		return o, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO organization_events(organization_id,actor_id,target_user_id,action,after_json)
 VALUES($1,$2,$2,'organization_created','{"role":"owner","status":"active","revision":1}')`, id, actorID); err != nil {
		return o, err
	}
	return lockScope(ctx, tx, actorID, id)
}
