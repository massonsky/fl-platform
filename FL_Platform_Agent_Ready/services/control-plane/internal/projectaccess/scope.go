package projectaccess

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const projectColumns = `p.id,coalesce(p.organization_id::text,''),p.owner_id,coalesce(p.created_by,p.owner_id),p.slug,p.name,
 p.access_mode,p.visibility,p.acl_revision,p.created_at,coalesce(g.role,''),coalesce(g.status,'')`

type projectRecord struct {
	Project
	grantRole, grantStatus string
}

func scanProject(row pgx.Row) (projectRecord, error) {
	var p projectRecord
	err := row.Scan(&p.ID, &p.OrganizationID, &p.OwnerID, &p.CreatedBy, &p.Slug, &p.Name, &p.AccessMode, &p.Visibility, &p.ACLRevision, &p.CreatedAt, &p.grantRole, &p.grantStatus)
	return p, err
}
func projectPolicy(p projectRecord, actor string, scope Policy) Project {
	scope.Mode, scope.Visibility = p.AccessMode, p.Visibility
	scope.Owner, scope.Creator = sameID(p.OwnerID, actor), sameID(p.CreatedBy, actor)
	scope.GrantRole, scope.GrantStatus = p.grantRole, p.grantStatus
	p.Capabilities, p.AccessSource = Evaluate(scope)
	if p.AccessMode == "organization" && p.Capabilities.CreateExperiment {
		p.RunBlockedReason = "ORG_AGENTS_NOT_READY"
	}
	return p.Project
}
func loadProject(ctx context.Context, tx pgx.Tx, actor string, ref ResourceRef) (Project, error) {
	var from, where string
	switch ref.Kind {
	case "project":
		from, where = "projects p", "p.id=$2"
	case "experiment":
		from, where = "experiments e JOIN projects p ON p.id=e.project_id", "e.id=$2"
	case "run":
		from, where = "runs r JOIN experiments e ON e.id=r.experiment_id JOIN projects p ON p.id=e.project_id", "r.id=$2"
	default:
		return Project{}, failure(400, "INVALID_REQUEST")
	}
	globalRole, err := identityRole(ctx, tx, actor)
	if err != nil {
		return Project{}, err
	}
	p, err := scanProject(tx.QueryRow(ctx, "SELECT "+projectColumns+" FROM "+from+
		" LEFT JOIN project_members g ON g.project_id=p.id AND g.user_id=$1 WHERE "+where, actor, ref.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, failure(404, "NOT_FOUND")
	}
	if err != nil {
		return Project{}, err
	}
	scope := Policy{GlobalRole: globalRole}
	if p.AccessMode == "legacy_private" {
		if ref.OrganizationID != "" {
			return Project{}, failure(404, "NOT_FOUND")
		}
	} else {
		if ref.OrganizationID == "" || !sameID(p.OrganizationID, ref.OrganizationID) {
			return Project{}, failure(404, "NOT_FOUND")
		}
		scope, err = organizationPolicy(ctx, tx, actor, ref.OrganizationID)
		if err != nil {
			return Project{}, err
		}
	}
	return projectPolicy(p, actor, scope), nil
}

// Within is the authorization boundary for protected work, including replay.
// Callbacks must not mutate ACL or try to upgrade the shared guard to exclusive.
// Session binding and public routing are a separate COM-02C gate.
func (s Store) Within(ctx context.Context, actor string, ref ResourceRef, action Action, fn func(pgx.Tx, Project) error) error {
	if !validIDs(actor, ref.ID) || ref.OrganizationID != "" && !validIDs(ref.OrganizationID) {
		return failure(400, "INVALID_REQUEST")
	}
	tx, err := s.begin(ctx, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	p, err := Check(ctx, tx, actor, ref, action)
	if err != nil {
		return err
	}
	if fn != nil {
		if err = fn(tx, p); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Check composes with the caller's existing transaction. The caller must acquire
// authorization.Read/Write first and authenticate its session in that transaction.
// actor alone is not proof of an authenticated public request.
func Check(ctx context.Context, tx pgx.Tx, actor string, ref ResourceRef, action Action) (Project, error) {
	if !validIDs(actor, ref.ID) || ref.OrganizationID != "" && !validIDs(ref.OrganizationID) {
		return Project{}, failure(400, "INVALID_REQUEST")
	}
	p, err := loadProject(ctx, tx, actor, ref)
	if err != nil {
		return p, err
	}
	return p, allowed(p, action)
}
func (s Store) Get(ctx context.Context, actor string, ref ResourceRef) (Project, error) {
	var output Project
	err := s.Within(ctx, actor, ref, Read, func(_ pgx.Tx, p Project) error { output = p; return nil })
	return output, err
}
func (s Store) List(ctx context.Context, actor, org string, limit int) ([]Project, error) {
	if !validIDs(actor, org) || limit < 1 || limit > 100 {
		return nil, failure(400, "INVALID_REQUEST")
	}
	tx, err := s.begin(ctx, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope, err := organizationPolicy(ctx, tx, actor, org)
	if err != nil {
		return nil, err
	}
	if scope.OrganizationKind != "shared" {
		return nil, failure(404, "NOT_FOUND")
	}
	admin := scope.OrganizationRole == "owner" || scope.OrganizationRole == "admin"
	rows, err := tx.Query(ctx, "SELECT "+projectColumns+` FROM projects p LEFT JOIN project_members g ON g.project_id=p.id AND g.user_id=$1
 WHERE p.organization_id=$2 AND p.access_mode='organization' AND
 ($3 OR (coalesce(g.status,'')<>'revoked' AND (g.status='active' OR p.created_by=$1 OR p.visibility='organization')))
 ORDER BY p.created_at,p.id LIMIT $4`, actor, org, admin, limit)
	if err != nil {
		return nil, err
	}
	items := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, projectPolicy(p, actor, scope))
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return items, tx.Commit(ctx)
}
