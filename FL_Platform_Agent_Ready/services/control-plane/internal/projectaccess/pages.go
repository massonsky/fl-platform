package projectaccess

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

// Page options and continuations are internal, decoded by a scope-bound signed
// cursor adapter. They must never be accepted as unsigned public query fields.
type ProjectKey struct {
	CreatedAt time.Time
	ID        string
}
type PageInput struct {
	Limit int
	Cut   time.Time
	After *ProjectKey
}
type PageContext struct {
	OrganizationID    string `json:"organization_id"`
	OrganizationState string `json:"organization_state"`
	Capabilities      struct {
		CreateProjects bool `json:"can_create_projects"`
	} `json:"capabilities"`
}
type ProjectPage struct {
	Items   []Project   `json:"items"`
	Context PageContext `json:"context"`
	Cut     time.Time   `json:"-"`
	Next    *ProjectKey `json:"-"`
}
type MemberPageInput struct {
	Limit   int
	Cut     time.Time
	AfterID string
}

func pageWindow(ctx context.Context, tx pgx.Tx, limit int, cut time.Time) (int, time.Time, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return 0, cut, failure(400, "INVALID_REQUEST")
	}
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return 0, cut, err
	}
	if cut.IsZero() {
		cut = now
	}
	if cut.After(now) || cut.Before(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return 0, cut, failure(400, "INVALID_REQUEST")
	}
	return limit, cut.UTC(), nil
}

func pageContext(org string, policy Policy) PageContext {
	result := PageContext{OrganizationID: org, OrganizationState: policy.OrganizationState}
	result.Capabilities.CreateProjects = policy.OrganizationKind == "shared" && policy.OrganizationState == "active" &&
		(policy.GlobalRole == "admin" || policy.GlobalRole == "researcher") &&
		(policy.OrganizationRole == "owner" || policy.OrganizationRole == "admin") && policy.MembershipStatus == "active"
	return result
}

func (s Service) Page(ctx context.Context, principal identity.Principal, org string, in PageInput) (ProjectPage, error) {
	return serviceCall(ctx, s, principal, false, func(ctx context.Context, tx pgx.Tx, user identity.User) (ProjectPage, error) {
		return projectPageTx(ctx, tx, user.ID, org, in)
	})
}

func projectPageTx(ctx context.Context, tx pgx.Tx, actor, org string, in PageInput) (ProjectPage, error) {
	var result ProjectPage
	if !validIDs(org) || in.After != nil && (in.Cut.IsZero() || !validIDs(in.After.ID) || in.After.CreatedAt.IsZero() || in.After.CreatedAt.After(in.Cut)) {
		return result, failure(400, "INVALID_REQUEST")
	}
	limit, cut, err := pageWindow(ctx, tx, in.Limit, in.Cut)
	if err != nil {
		return result, err
	}
	policy, err := organizationPolicy(ctx, tx, actor, org)
	if err != nil {
		return result, err
	}
	if policy.OrganizationKind != "shared" {
		return result, failure(404, "NOT_FOUND")
	}
	var afterTime, afterID any
	if in.After != nil {
		afterTime, afterID = in.After.CreatedAt, in.After.ID
	}
	admin := policy.OrganizationRole == "owner" || policy.OrganizationRole == "admin"
	rows, err := tx.Query(ctx, "SELECT "+projectColumns+` FROM projects p
 LEFT JOIN project_members g ON g.project_id=p.id AND g.user_id=$1
 WHERE p.organization_id=$2 AND p.access_mode='organization' AND p.created_at<=$3
 AND ($4::timestamptz IS NULL OR (p.created_at,p.id)>($4,$5::uuid))
 AND ($6 OR (coalesce(g.status,'')<>'revoked' AND (g.status='active' OR p.created_by=$1 OR p.visibility='organization')))
 ORDER BY p.created_at,p.id LIMIT $7`, actor, org, cut, afterTime, afterID, admin, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result = ProjectPage{Items: []Project{}, Context: pageContext(org, policy), Cut: cut}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return ProjectPage{}, err
		}
		result.Items = append(result.Items, projectPolicy(p, actor, policy))
	}
	if err = rows.Err(); err != nil {
		return ProjectPage{}, err
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		last := result.Items[limit-1]
		result.Next = &ProjectKey{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return result, nil
}
