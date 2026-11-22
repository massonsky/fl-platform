//go:build integration

package transport

import (
	"context"
	"strconv"
	"testing"

	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

func TestMembershipProjectPermanentRevokeAndReactivation(t *testing.T) {
	f := newMembershipProjects(t)
	before := map[string]string{}
	for kind := range f.projects {
		before[kind] = f.acl(t, kind)
	}
	f.change(t, "researcher", "revoked")
	for kind, want := range map[string]string{"explicit": "researcher/revoked/2", "capped": "viewer/revoked/2", "creator": "viewer/revoked/1", "denied": "researcher/revoked/1", "foreign": "researcher/active/1", "visible": "absent", "legacy": "absent"} {
		if got := f.grantState(t, kind); got != want {
			t.Fatalf("%s got%s want%s", kind, got, want)
		}
		wantACL := before[kind]
		if kind == "explicit" || kind == "capped" || kind == "creator" {
			n, _ := strconv.Atoi(wantACL)
			wantACL = strconv.Itoa(n + 1)
		}
		if f.acl(t, kind) != wantACL {
			t.Fatal("ACL revision changed incorrectly", kind)
		}
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM project_access_events WHERE after_json->>'reason'='membership_revoked'") != "3" {
		t.Fatal("cascade audit must include exactly3changed projects")
	}
	if authorizationValue(t, f.pool, "SELECT before_json::text FROM project_access_events WHERE project_id=$1 AND after_json->>'reason'='membership_revoked'", f.projects["creator"]) != "{}" {
		t.Fatal("creator deny invented a previous grant")
	}
	f.change(t, "researcher", "revoked") // Current-revision no-op must not repeat cascade.
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM project_access_events WHERE after_json->>'reason'='membership_revoked'") != "3" {
		t.Fatal("no-op repeated cascade audit")
	}
	if _, err := f.s.Identity.Authenticate(context.Background(), f.users["other"].ID); err != nil {
		t.Fatal("org revoke logged out personal session", err)
	}
	f.read(t, "foreign", true)
	_, run := projectResources(t, f.authorizationFixture, f.projects["legacy"], f.users["other"].ID)
	if _, err := f.s.Experiments.Detail(context.Background(), experiment.Access{Principal: organizationPrincipal(f.authorizationFixture, "other")}, "runs", run); err != nil {
		t.Fatal("legacy private read changed", err)
	}
	f.change(t, "researcher", "active")
	for _, kind := range []string{"explicit", "capped", "creator", "denied", "race"} {
		f.read(t, kind, false)
	}
	f.read(t, "visible", true)
	f.read(t, "foreign", true)
	store := projectaccess.Store{Pool: f.pool}
	_, err := store.SaveGrant(context.Background(), f.users["owner"].ID, f.org, f.projects["explicit"], f.users["other"].ID, projectaccess.GrantInput{Role: "researcher", Status: "active", Revision: 1})
	if e, ok := err.(projectaccess.Failure); !ok || e.Code != "VERSION_CONFLICT" {
		t.Fatal("stale grant resurrected", err)
	}
	if _, err = store.SaveGrant(context.Background(), f.users["owner"].ID, f.org, f.projects["explicit"], f.users["other"].ID, projectaccess.GrantInput{Role: "viewer", Status: "active", Revision: 2}); err != nil {
		t.Fatal("explicit new permission", err)
	}
	f.read(t, "explicit", true)
	f.change(t, "owner", "active")
	for _, kind := range []string{"capped", "creator", "denied", "race"} {
		f.read(t, kind, true)
	}
	if f.grantState(t, "creator") != "viewer/revoked/1" {
		t.Fatal("admin inheritance rewrote deny history")
	}
}

func TestMembershipProjectTemporarySuspensionAndRollback(t *testing.T) {
	f := newMembershipProjects(t)
	before := f.acl(t, "explicit")
	f.change(t, "researcher", "suspended")
	f.read(t, "explicit", false)
	if f.grantState(t, "explicit") != "researcher/active/1" || f.acl(t, "explicit") != before {
		t.Fatal("temporary suspension became permanent")
	}
	f.change(t, "researcher", "active")
	f.read(t, "explicit", true)
	authorizationExec(t, f.pool, `CREATE FUNCTION reject_cascade_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.after_json->>'reason'='membership_revoked' THEN RAISE EXCEPTION 'fixture audit failure'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER reject_cascade_event BEFORE INSERT ON project_access_events FOR EACH ROW EXECUTE FUNCTION reject_cascade_event()`)
	_, err := (organization.Service{Pool: f.pool}).ChangeMember(context.Background(), organizationPrincipal(f.authorizationFixture, "owner"), f.org, f.users["other"].ID, organization.MemberChange{Role: "researcher", Status: "revoked", Revision: f.membership.Revision})
	organizationFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if f.grantState(t, "explicit") != "researcher/active/1" || f.grantState(t, "creator") != "absent" || f.acl(t, "explicit") != before {
		t.Fatal("audit failure committed partial grant state")
	}
	if authorizationValue(t, f.pool, "SELECT status||'/'||revision FROM organization_memberships WHERE organization_id=$1 AND user_id=$2", f.org, f.users["other"].ID) != "active/"+strconv.Itoa(f.membership.Revision) {
		t.Fatal("audit failure committed membership")
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM project_access_events WHERE after_json->>'reason'='membership_revoked'") != "0" {
		t.Fatal("rollback retained cascade audit")
	}
	authorizationExec(t, f.pool, "DROP TRIGGER reject_cascade_event ON project_access_events")
	_, err = (organization.Service{Pool: f.pool}).ChangeMember(context.Background(), organizationPrincipal(f.authorizationFixture, "owner"), f.org, f.users["owner"].ID, organization.MemberChange{Role: "owner", Status: "revoked", Revision: 1})
	organizationFailure(t, err, 409, "LAST_OWNER")
	if f.grantState(t, "explicit") != "researcher/active/1" {
		t.Fatal("last owner denial touched grants")
	}
}
