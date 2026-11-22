//go:build integration

package transport

import (
	"context"
	"testing"

	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
)

func TestAdministrationServiceDependencyAndCommitFailuresRollback(t *testing.T) {
	f := authorizationDB(t, false)
	ctx := context.Background()
	s := configureCredentialAdministration(t, f.s)
	p := f.users["admin"].Principal()
	disabled := false
	input := admin.Input{Role: "viewer", Disabled: &disabled, Password: "valid-new-password"}
	before := adminSessionSnapshot(t, f)
	authorizationExec(t, f.pool, "ALTER TABLE administration_events RENAME TO unavailable_admin_events")
	items, more, err := s.Page(ctx, p, "events", 0, 50)
	adminSessionFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if items != nil || more {
		t.Fatal("failed page returned partial result")
	}
	got, err := s.MutateCredential(ctx, p, administrationCredentialCommand("user.update", f.users["owner"].ID, "admin-outage-key", "outage-fingerprint", input))
	adminSessionFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if got != nil {
		t.Fatal("failed mutation returned DTO")
	}
	authorizationHTTP(t, f.s, f.users["admin"], "admin/events", "", 503, "DEPENDENCY_UNAVAILABLE")
	authorizationExec(t, f.pool, "ALTER TABLE unavailable_admin_events RENAME TO administration_events")
	if adminSessionSnapshot(t, f) != before {
		t.Fatal("dependency failure changed users/sessions/receipts/audit")
	}
	authorizationExec(t, f.pool, `CREATE FUNCTION reject_admin_commit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'isolated deferred failure'; END$$;
 CREATE CONSTRAINT TRIGGER reject_admin_commit AFTER INSERT ON administration_receipts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_admin_commit()`)
	got, err = s.MutateCredential(ctx, p, administrationCredentialCommand("user.update", f.users["owner"].ID, "admin-commit-key", "commit-fingerprint", input))
	adminSessionFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if got != nil || adminSessionSnapshot(t, f) != before {
		t.Fatal("commit failure returned DTO or committed state")
	}
}

func TestAdministrationServicePreservesOwnershipAndConflictGuards(t *testing.T) {
	f := projectFixture(t, true)
	configureCredentialAdministration(t, f.s)
	org := f.shared(t)
	owner, manager := f.users["owner"], f.users["admin"]
	before := adminSessionSnapshot(t, f)
	authorizationHTTP(t, f.s, manager, "admin/users/"+manager.ID, `{"role":"viewer","disabled":false}`, 409, "LAST_ADMIN")
	authorizationHTTP(t, f.s, manager, "admin/users/"+owner.ID, `{"role":"viewer","disabled":false}`, 409, "LAST_OWNER")
	authorizationHTTP(t, f.s, manager, "admin/users/"+owner.ID, `{"role":"researcher","disabled":true,"password":"valid-new-password"}`, 409, "LAST_OWNER")
	if adminSessionSnapshot(t, f) != before {
		t.Fatal("ownership rejection changed state")
	}
	authorizationHTTP(t, f.s, manager, "admin/users", `{"username":"owner","name":"Conflict","role":"viewer","password":"valid-new-password"}`, 409, "USERNAME_CONFLICT")
	authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", org, f.users["other"].ID)
	authorizationHTTP(t, f.s, manager, "admin/users/"+owner.ID, `{"role":"researcher","disabled":true}`, 200, "")
	authorizationHTTP(t, f.s, owner, "account/password", `{"current_password":"valid-old-password","password":"valid-new-password"}`, 401, "UNAUTHORIZED")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM sessions WHERE user_id=$1", owner.ID) != "0" {
		t.Fatal("disabled user sessions retained")
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_receipts") != "1" || authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_events") != "1" {
		t.Fatal("denied operations emitted receipts/audit")
	}
}
