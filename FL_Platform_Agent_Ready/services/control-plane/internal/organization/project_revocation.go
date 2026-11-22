package organization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// revokeProjectAccess is part of the membership invariant. The caller must own
// authorization.Write and the membership transaction; never start a nested TX.
// Schema7 has no shared project mode. Partial schema8 must not silently skip it.
func revokeProjectAccess(ctx context.Context, tx pgx.Tx, org, actor, target string) error {
	var columns int
	var members, events bool
	err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM pg_attribute WHERE attrelid='public.projects'::regclass
 AND attname IN ('created_by','access_mode','visibility','acl_revision') AND NOT attisdropped),
 to_regclass('public.project_members') IS NOT NULL,
 to_regclass('public.project_access_events') IS NOT NULL`).Scan(&columns, &members, &events)
	if err != nil {
		return err
	}
	if columns == 0 && !members && !events {
		return nil
	}
	if columns != 4 || !members || !events {
		return failure(503, "DEPENDENCY_UNAVAILABLE")
	}
	// Materialized before-state and ordered project/grant locks make audit/CAS
	// deterministic. Work stays in PostgreSQL, with no unbounded Go row collector.
	// Existing denies, inherited organization visibility and legacy private rows
	// are excluded; an absent creator grant becomes an explicit permanent deny.
	_, err = tx.Exec(ctx, `WITH affected AS MATERIALIZED (
 SELECT p.id,p.organization_id,g.role,g.status,g.revision
 FROM projects p LEFT JOIN project_members g
 ON g.project_id=p.id AND g.organization_id=p.organization_id AND g.user_id=$3
 WHERE p.organization_id=$1 AND p.access_mode='organization'
 AND (g.status='active' OR (p.created_by=$3 AND g.user_id IS NULL))
 ORDER BY p.id FOR UPDATE OF p
), changed AS (
 INSERT INTO project_members(project_id,organization_id,user_id,role,status,revision,granted_by)
 SELECT a.id,a.organization_id,$3,coalesce(a.role,'viewer'),'revoked',1,$2
 FROM affected a ORDER BY a.id
 ON CONFLICT(project_id,user_id) DO UPDATE
 SET status='revoked',revision=project_members.revision+1,granted_by=EXCLUDED.granted_by,updated_at=now()
 WHERE project_members.status='active'
 RETURNING project_id,role,status,revision
), audited AS (
 INSERT INTO project_access_events(project_id,organization_id,actor_id,target_user_id,action,before_json,after_json)
 SELECT c.project_id,$1,$2,$3,'grant_changed',
 CASE WHEN a.revision IS NULL THEN '{}'::jsonb ELSE
 jsonb_build_object('role',a.role,'status',a.status,'revision',a.revision) END,
 jsonb_build_object('role',c.role,'status',c.status,'revision',c.revision,'reason','membership_revoked')
 FROM changed c JOIN affected a ON a.id=c.project_id
 RETURNING project_id
)
 UPDATE projects p SET acl_revision=p.acl_revision+1
 FROM audited a WHERE p.id=a.project_id`, org, actor, target)
	return err
}
