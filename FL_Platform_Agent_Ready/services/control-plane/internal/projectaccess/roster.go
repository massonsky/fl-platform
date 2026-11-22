package projectaccess

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

type AccessMember struct {
	UserID             string       `json:"user_id"`
	DisplayName        string       `json:"display_name"`
	GlobalRole         string       `json:"global_role"`
	OrganizationRole   string       `json:"organization_role"`
	MembershipStatus   string       `json:"membership_status"`
	AccountActive      bool         `json:"account_active"`
	Grant              *Grant       `json:"grant"`
	Capabilities       Capabilities `json:"capabilities"`
	AccessSource       string       `json:"access_source"`
	CanGrant           bool         `json:"can_grant"`
	CanRevoke          bool         `json:"can_revoke"`
	GrantBlockedReason string       `json:"grant_blocked_reason,omitempty"`
	WriteCapReason     string       `json:"write_cap_reason,omitempty"`
}
type AccessPage struct {
	Project    Project        `json:"project"`
	Items      []AccessMember `json:"items"`
	Cut        time.Time      `json:"-"`
	NextUserID string         `json:"-"`
}

func memberCapabilities(member *AccessMember, project Project, scope Policy) {
	scope.GlobalRole, scope.Disabled = member.GlobalRole, !member.AccountActive
	scope.OrganizationRole, scope.MembershipStatus = member.OrganizationRole, member.MembershipStatus
	scope.Visibility, scope.Creator = project.Visibility, sameID(project.CreatedBy, member.UserID)
	if member.Grant != nil {
		scope.GrantRole, scope.GrantStatus = member.Grant.Role, member.Grant.Status
	}
	member.Capabilities, member.AccessSource = Evaluate(scope)
	member.CanRevoke = project.Capabilities.ManageAccess && member.OrganizationRole != "owner" && member.OrganizationRole != "admin"
	member.CanGrant = member.CanRevoke && member.AccountActive && member.MembershipStatus == "active"
	switch {
	case member.OrganizationRole == "owner" || member.OrganizationRole == "admin":
		member.GrantBlockedReason = "INHERITED_ACCESS"
	case !project.Capabilities.ManageAccess:
		member.GrantBlockedReason = "READ_ONLY"
	case !member.AccountActive || member.MembershipStatus != "active":
		member.GrantBlockedReason = "MEMBER_NOT_ACTIVE"
	}
	switch {
	case !member.AccountActive || member.MembershipStatus != "active":
		member.WriteCapReason = "MEMBER_NOT_ACTIVE"
	case scope.OrganizationState != "active":
		member.WriteCapReason = "ORGANIZATION_SUSPENDED"
	case member.GlobalRole == "viewer":
		member.WriteCapReason = "GLOBAL_VIEWER"
	case member.OrganizationRole == "viewer":
		member.WriteCapReason = "ORGANIZATION_VIEWER"
	}
}

func (s Service) Roster(ctx context.Context, principal identity.Principal, org, project string, in MemberPageInput) (AccessPage, error) {
	return s.memberPage(ctx, principal, org, project, in, false)
}
func (s Service) Candidates(ctx context.Context, principal identity.Principal, org, project string, in MemberPageInput) (AccessPage, error) {
	return s.memberPage(ctx, principal, org, project, in, true)
}
func (s Service) memberPage(ctx context.Context, principal identity.Principal, org, project string, in MemberPageInput, candidates bool) (AccessPage, error) {
	return serviceCall(ctx, s, principal, false, func(ctx context.Context, tx pgx.Tx, user identity.User) (AccessPage, error) {
		return memberPageTx(ctx, tx, user.ID, org, project, in, candidates)
	})
}

func memberPageTx(ctx context.Context, tx pgx.Tx, actor, org, project string, in MemberPageInput, candidates bool) (AccessPage, error) {
	var result AccessPage
	if !validIDs(org, project) || in.AfterID != "" && (!validIDs(in.AfterID) || in.Cut.IsZero()) {
		return result, failure(400, "INVALID_REQUEST")
	}
	limit, cut, err := pageWindow(ctx, tx, in.Limit, in.Cut)
	if err != nil {
		return result, err
	}
	p, err := Check(ctx, tx, actor, ResourceRef{Kind: "project", ID: project, OrganizationID: org}, Read)
	if err != nil {
		return result, err
	}
	scope, err := organizationPolicy(ctx, tx, actor, org)
	if err != nil {
		return result, err
	}
	if scope.OrganizationRole != "owner" && scope.OrganizationRole != "admin" {
		return result, failure(403, "FORBIDDEN")
	}
	var after any
	if in.AfterID != "" {
		after = in.AfterID
	}
	rows, err := tx.Query(ctx, `SELECT u.id,u.display_name,u.role,u.disabled_at IS NULL,m.role,m.status,
 coalesce(g.role,''),coalesce(g.status,''),coalesce(g.revision,0),coalesce(g.updated_at,m.updated_at)
 FROM organization_memberships m JOIN users u ON u.id=m.user_id
 LEFT JOIN project_members g ON g.organization_id=m.organization_id AND g.user_id=m.user_id AND g.project_id=$2
 WHERE m.organization_id=$1 AND m.created_at<=$3 AND ($4::uuid IS NULL OR m.user_id>$4)
 AND (NOT $5 OR (m.status='active' AND u.disabled_at IS NULL AND m.role IN ('researcher','viewer')))
 ORDER BY m.user_id LIMIT $6`, org, project, cut, after, candidates, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result = AccessPage{Project: p, Items: []AccessMember{}, Cut: cut}
	for rows.Next() {
		var member AccessMember
		var grant Grant
		if err = rows.Scan(&member.UserID, &member.DisplayName, &member.GlobalRole, &member.AccountActive,
			&member.OrganizationRole, &member.MembershipStatus, &grant.Role, &grant.Status, &grant.Revision, &grant.UpdatedAt); err != nil {
			return AccessPage{}, err
		}
		if grant.Revision > 0 {
			grant.UserID = member.UserID
			member.Grant = &grant
		}
		memberCapabilities(&member, p, scope)
		result.Items = append(result.Items, member)
	}
	if err = rows.Err(); err != nil {
		return AccessPage{}, err
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		result.NextUserID = result.Items[limit-1].UserID
	}
	return result, nil
}
