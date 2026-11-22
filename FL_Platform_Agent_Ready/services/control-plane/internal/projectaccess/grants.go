package projectaccess

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func scanGrant(row pgx.Row) (Grant, error) {
	var g Grant
	err := row.Scan(&g.UserID, &g.Role, &g.Status, &g.Revision, &g.UpdatedAt)
	return g, err
}
func grantAudit(g Grant) map[string]any {
	return map[string]any{"role": g.Role, "status": g.Status, "revision": g.Revision}
}
func managedProject(ctx context.Context, tx pgx.Tx, actor, org, id string) (Project, error) {
	p, err := loadProject(ctx, tx, actor, ResourceRef{Kind: "project", ID: id, OrganizationID: org})
	if err != nil {
		return p, err
	}
	return p, allowed(p, ManageAccess)
}

func (s Store) SaveGrant(ctx context.Context, actor, org, project, target string, in GrantInput) (Grant, error) {
	tx, err := s.begin(ctx, true)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(ctx)
	output, err := saveGrantTx(ctx, tx, actor, org, project, target, in)
	if err != nil {
		return Grant{}, err
	}
	return output, tx.Commit(ctx)
}

func saveGrantTx(ctx context.Context, tx pgx.Tx, actor, org, project, target string, in GrantInput) (Grant, error) {
	var output Grant
	if !validIDs(actor, org, project, target) || in.Revision < 0 || in.Revision > 2147483647 ||
		(in.Role != "researcher" && in.Role != "viewer") || (in.Status != "active" && in.Status != "revoked") {
		return output, failure(400, "INVALID_REQUEST")
	}
	p, err := managedProject(ctx, tx, actor, org, project)
	if err != nil {
		return output, err
	}
	var memberRole, memberStatus string
	var disabled bool
	err = tx.QueryRow(ctx, `SELECT m.role,m.status,u.disabled_at IS NOT NULL FROM organization_memberships m JOIN users u ON u.id=m.user_id
 WHERE m.organization_id=$1 AND m.user_id=$2 FOR SHARE OF m,u`, org, target).Scan(&memberRole, &memberStatus, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return output, failure(404, "NOT_FOUND")
	}
	if err != nil {
		return output, err
	}
	if memberRole == "owner" || memberRole == "admin" {
		return output, failure(409, "INHERITED_ACCESS")
	}
	if in.Status == "active" && (memberStatus != "active" || disabled) {
		return output, failure(409, "MEMBER_NOT_ACTIVE")
	}
	current, err := scanGrant(tx.QueryRow(ctx, `SELECT user_id,role,status,revision,updated_at FROM project_members
 WHERE project_id=$1 AND user_id=$2 FOR UPDATE`, project, target))
	before := map[string]any{}
	if errors.Is(err, pgx.ErrNoRows) {
		if in.Revision != 0 {
			return output, failure(409, "VERSION_CONFLICT")
		}
		output, err = scanGrant(tx.QueryRow(ctx, `INSERT INTO project_members(project_id,organization_id,user_id,role,status,granted_by)
 VALUES($1,$2,$3,$4,$5,$6) RETURNING user_id,role,status,revision,updated_at`, project, org, target, in.Role, in.Status, actor))
	} else if err != nil {
		return output, err
	} else {
		if current.Revision != in.Revision {
			return output, failure(409, "VERSION_CONFLICT")
		}
		if current.Role == in.Role && current.Status == in.Status {
			return current, nil
		}
		before = grantAudit(current)
		output, err = scanGrant(tx.QueryRow(ctx, `UPDATE project_members SET role=$3,status=$4,revision=revision+1,granted_by=$5,updated_at=now()
 WHERE project_id=$1 AND user_id=$2 RETURNING user_id,role,status,revision,updated_at`, project, target, in.Role, in.Status, actor))
	}
	if err != nil {
		return output, err
	}
	if err = event(ctx, tx, p, actor, target, "grant_changed", before, grantAudit(output)); err != nil {
		return output, err
	}
	if _, err = tx.Exec(ctx, "UPDATE projects SET acl_revision=acl_revision+1 WHERE id=$1", project); err != nil {
		return output, err
	}
	return output, nil
}

func (s Store) SetVisibility(ctx context.Context, actor, org, project string, in VisibilityInput) (Project, error) {
	tx, err := s.begin(ctx, true)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	output, err := setVisibilityTx(ctx, tx, actor, org, project, in)
	if err != nil {
		return Project{}, err
	}
	return output, tx.Commit(ctx)
}

func setVisibilityTx(ctx context.Context, tx pgx.Tx, actor, org, project string, in VisibilityInput) (Project, error) {
	var output Project
	if !validIDs(actor, org, project) || in.ACLRevision < 1 || in.ACLRevision > 2147483647 || (in.Visibility != "private" && in.Visibility != "organization") {
		return output, failure(400, "INVALID_REQUEST")
	}
	p, err := managedProject(ctx, tx, actor, org, project)
	if err != nil {
		return output, err
	}
	if p.ACLRevision != in.ACLRevision {
		return output, failure(409, "VERSION_CONFLICT")
	}
	if p.Visibility == in.Visibility {
		return p, nil
	}
	if _, err = tx.Exec(ctx, "UPDATE projects SET visibility=$2,acl_revision=acl_revision+1 WHERE id=$1", project, in.Visibility); err != nil {
		return output, err
	}
	if err = event(ctx, tx, p, actor, "", "visibility_changed", map[string]any{"visibility": p.Visibility, "acl_revision": p.ACLRevision}, map[string]any{"visibility": in.Visibility, "acl_revision": p.ACLRevision + 1}); err != nil {
		return output, err
	}
	return loadProject(ctx, tx, actor, ResourceRef{Kind: "project", ID: project, OrganizationID: org})
}
