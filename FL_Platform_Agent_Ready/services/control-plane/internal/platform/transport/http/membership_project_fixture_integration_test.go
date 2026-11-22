//go:build integration

package transport

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

type membershipProjectFixture struct {
	authorizationFixture
	org, foreignOrg string
	projects        map[string]string
	membership      organization.Membership
}

func newMembershipProjects(t *testing.T) membershipProjectFixture {
	t.Helper()
	f := membershipProjectFixture{authorizationFixture: projectFixture(t, true), projects: map[string]string{}}
	f.org = f.shared(t)
	f.foreignOrg = f.shared(t)
	actor, target := f.users["owner"].ID, f.users["other"].ID
	authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$3,'admin'),($2,$3,'researcher')", f.org, f.foreignOrg, target)
	store := projectaccess.Store{Pool: f.pool}
	ctx := context.Background()
	for _, kind := range []string{"explicit", "capped", "creator", "visible", "denied", "foreign", "race"} {
		id, org, creator := uuid.NewString(), f.org, actor
		if kind == "foreign" {
			org = f.foreignOrg
		}
		if kind == "creator" {
			creator = target
		}
		if _, err := store.Create(ctx, creator, org, id, projectaccess.CreateInput{Slug: kind, Name: kind}); err != nil {
			t.Fatal(err)
		}
		f.projects[kind] = id
		if kind == "visible" || kind == "capped" {
			if _, err := store.SetVisibility(ctx, actor, org, id, projectaccess.VisibilityInput{Visibility: "organization", ACLRevision: 1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var err error
	f.membership, err = (organization.Service{Pool: f.pool}).ChangeMember(ctx, organizationPrincipal(f.authorizationFixture, "owner"), f.org, target, organization.MemberChange{Role: "researcher", Status: "active", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"explicit", "capped", "denied", "foreign"} {
		org, role, status := f.org, "researcher", "active"
		if kind == "capped" {
			role = "viewer"
		}
		if kind == "denied" {
			status = "revoked"
		}
		if kind == "foreign" {
			org = f.foreignOrg
		}
		if _, err = store.SaveGrant(ctx, actor, org, f.projects[kind], target, projectaccess.GrantInput{Role: role, Status: status, Revision: 0}); err != nil {
			t.Fatal(err)
		}
	}
	f.projects["legacy"] = uuid.NewString()
	authorizationExec(t, f.pool, "INSERT INTO projects(id,owner_id,slug,name) VALUES($1,$2,'cascade-legacy','Legacy')", f.projects["legacy"], target)
	return f
}

func (f *membershipProjectFixture) change(t *testing.T, role, status string) {
	t.Helper()
	m, err := (organization.Service{Pool: f.pool}).ChangeMember(context.Background(), organizationPrincipal(f.authorizationFixture, "owner"), f.org, f.users["other"].ID, organization.MemberChange{Role: role, Status: status, Revision: f.membership.Revision})
	if err != nil {
		t.Fatal(err)
	}
	f.membership = m
}

func (f membershipProjectFixture) read(t *testing.T, kind string, allowed bool) {
	t.Helper()
	org := f.org
	if kind == "foreign" {
		org = f.foreignOrg
	}
	_, err := (projectaccess.Service{Pool: f.pool}).Get(context.Background(), organizationPrincipal(f.authorizationFixture, "other"), org, f.projects[kind])
	if allowed && err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	if !allowed {
		var status int
		var code string
		if e, ok := err.(projectaccess.Failure); ok {
			status, code = e.Status, e.Code
		}
		if status != 404 || code != "NOT_FOUND" {
			t.Fatalf("%s: wanted404 got%v", kind, err)
		}
	}
}

func (f membershipProjectFixture) grantState(t *testing.T, kind string) string {
	t.Helper()
	return authorizationValue(t, f.pool, `SELECT coalesce((SELECT role||'/'||status||'/'||revision FROM project_members WHERE project_id=$1 AND user_id=$2),'absent')`, f.projects[kind], f.users["other"].ID)
}

func (f membershipProjectFixture) acl(t *testing.T, kind string) string {
	t.Helper()
	return authorizationValue(t, f.pool, "SELECT acl_revision::text FROM projects WHERE id=$1", f.projects[kind])
}
