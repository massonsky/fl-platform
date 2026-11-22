package adapters

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/legacylab"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

// Access is server-owned context. Existing HTTP routes always leave OrganizationID
// empty; explicit organization routing remains a separate, default-off release.
type Access struct {
	Principal      identity.Principal
	OrganizationID string
}

func accessFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, identity.ErrUnauthorized) {
		return Failure{401, "UNAUTHORIZED"}
	}
	var failure Failure
	if errors.As(err, &failure) {
		return err
	}
	var project projectaccess.Failure
	if errors.As(err, &project) {
		return Failure{project.Status, project.Code}
	}
	var lab legacylab.Failure
	if errors.As(err, &lab) {
		return Failure{lab.Status, lab.Code}
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return err // Keep the established slug/idempotency conflict mapper.
	}
	return Failure{503, "DEPENDENCY_UNAVAILABLE"}
}

// accessSchema distinguishes schema6/7 from a complete schema8. Partial ACL
// rollout is an unavailable dependency, never permission to use owner fallback.
func accessSchema(ctx context.Context, q db.Query) (bool, error) {
	var columns int
	var members, events, organizations, memberships, organizationID bool
	err := q.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM pg_attribute WHERE attrelid='public.projects'::regclass
 AND attname IN ('created_by','access_mode','visibility','acl_revision') AND NOT attisdropped),
 to_regclass('public.project_members') IS NOT NULL,
 to_regclass('public.project_access_events') IS NOT NULL,
 to_regclass('public.organizations') IS NOT NULL,
 to_regclass('public.organization_memberships') IS NOT NULL,
 EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='public.projects'::regclass AND attname='organization_id' AND NOT attisdropped)`).
		Scan(&columns, &members, &events, &organizations, &memberships, &organizationID)
	if err != nil {
		return false, accessFailure(err)
	}
	if columns == 0 && !members && !events {
		return false, nil
	}
	if columns != 4 || !members || !events || !organizations || !memberships || !organizationID {
		return false, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	return true, nil
}

func (s Store) protected(ctx context.Context, access Access, fn func(context.Context, pgx.Tx, identity.User, bool) error) error {
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	err := db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := authorization.Read(ctx, tx); err != nil {
			return err
		}
		user, err := identity.Current(ctx, tx, access.Principal)
		if err != nil {
			return err
		}
		acl, err := accessSchema(ctx, tx)
		if err != nil {
			return err
		}
		return fn(ctx, tx, user, acl)
	})
	return accessFailure(err)
}

func checkProject(ctx context.Context, tx pgx.Tx, access Access, user identity.User, acl bool, kind, id string, action projectaccess.Action) error {
	if acl {
		_, err := projectaccess.Check(ctx, tx, user.ID, projectaccess.ResourceRef{Kind: kind, ID: id, OrganizationID: access.OrganizationID}, action)
		return err
	}
	if access.OrganizationID != "" {
		return Failure{404, "NOT_FOUND"}
	}
	if err := legacyScope(ctx, tx, user.ID, kind, id, false); err != nil {
		return err
	}
	if action != projectaccess.Read && action != projectaccess.Export && user.Role == "viewer" {
		return Failure{403, "FORBIDDEN"}
	}
	return nil
}

func legacyScope(ctx context.Context, q db.Query, user, kind, id string, acl bool) error {
	var from, condition string
	switch kind {
	case "project", "projects":
		from, condition = "projects p", "p.id=$1"
	case "experiment", "experiments":
		from, condition = "experiments e JOIN projects p ON p.id=e.project_id", "e.id=$1"
	case "run", "runs":
		from, condition = "runs r JOIN experiments e ON e.id=r.experiment_id JOIN projects p ON p.id=e.project_id", "r.id=$1"
	default:
		return Failure{404, "NOT_FOUND"}
	}
	if acl {
		condition += " AND p.access_mode='legacy_private'"
	}
	var exists bool
	if err := q.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+from+" WHERE "+condition+" AND p.owner_id=$2)", id, user).Scan(&exists); err != nil {
		return accessFailure(err)
	}
	if !exists {
		return Failure{404, "NOT_FOUND"}
	}
	return nil
}

func authorizeMutation(ctx context.Context, tx pgx.Tx, access Access, user identity.User, acl bool, operation, id string) error {
	var kind string
	var action projectaccess.Action
	switch operation {
	case "project":
		if access.OrganizationID != "" {
			return Failure{404, "NOT_FOUND"}
		}
		if user.Role == "viewer" {
			return Failure{403, "FORBIDDEN"}
		}
		return legacylab.Require(ctx, tx, user)
	case "experiment":
		kind, action = "project", projectaccess.CreateExperiment
	case "queue":
		kind, action = "experiment", projectaccess.QueueRun
	case "cancel":
		kind, action = "run", projectaccess.CancelRun
	default:
		return Failure{404, "NOT_FOUND"}
	}
	if err := checkProject(ctx, tx, access, user, acl, kind, id, action); err != nil {
		return err
	}
	if access.OrganizationID == "" && operation != "cancel" {
		return legacylab.Require(ctx, tx, user)
	}
	return nil
}
