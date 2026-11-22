//go:build integration

package main

import (
	"context"
	"testing"

	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/legacylab"
)

const bootstrapAdmissionSnapshot = `SELECT jsonb_build_object(
 'users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),
 'grants',(SELECT jsonb_agg(to_jsonb(a) ORDER BY user_id) FROM legacy_lab_admissions a),
 'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM legacy_lab_admission_events e))::text`

func TestBootstrapInitialAdmissionAndRevokedRerun(t *testing.T) {
	pool, ctx := bootstrapDatabase(t), context.Background()
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	passwordHash := identity.PasswordHash("isolated-initial-password")
	if err := bootstrapAdmin(ctx, pool, passwordHash); err != nil {
		t.Fatal(err)
	}
	if bootstrapScalar(t, pool, "SELECT count(*)::text FROM users") != "1" ||
		bootstrapScalar(t, pool, "SELECT status||':'||source||':'||revision::text FROM legacy_lab_admissions") != "active:bootstrap:1" ||
		bootstrapScalar(t, pool, "SELECT count(*)::text FROM legacy_lab_admission_events WHERE action='bootstrap_admitted' AND actor_id=target_user_id") != "1" {
		t.Fatal("fresh bootstrap did not atomically admit only initial administrator")
	}
	before := bootstrapScalar(t, pool, bootstrapAdmissionSnapshot)
	if err := bootstrapAdmin(ctx, pool, identity.PasswordHash("different-ignored-password")); err != nil {
		t.Fatal(err)
	}
	if bootstrapScalar(t, pool, bootstrapAdmissionSnapshot) != before {
		t.Fatal("repeated bootstrap changed credentials/admission/audit")
	}
	id := bootstrapScalar(t, pool, "SELECT id::text FROM users WHERE external_subject='admin'")
	p := identity.Principal{UserID: id, SessionHash: identity.Hash("isolated-bootstrap-session")}
	bootstrapExec(t, pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')", p.SessionHash, id)
	if _, err := (legacylab.Service{Pool: pool}).Change(ctx, p, id, legacylab.Change{Status: "revoked", Revision: 1, Reason: "Intentional operator revocation"}); err != nil {
		t.Fatal(err)
	}
	before = bootstrapScalar(t, pool, bootstrapAdmissionSnapshot)
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapAdmin(ctx, pool, passwordHash); err != nil {
		t.Fatal(err)
	}
	if bootstrapScalar(t, pool, bootstrapAdmissionSnapshot) != before || bootstrapScalar(t, pool, "SELECT status FROM legacy_lab_admissions") != "revoked" {
		t.Fatal("migration/bootstrap rerun undid intentional revocation")
	}
	bootstrapExec(t, pool, "INSERT INTO users(external_subject,display_name,role) VALUES('later-admin','Later admin','admin')")
	if err := bootstrapAdmin(ctx, pool, passwordHash); err != nil {
		t.Fatal(err)
	}
	if bootstrapScalar(t, pool, "SELECT count(*)::text FROM legacy_lab_admissions") != "1" {
		t.Fatal("bootstrap implicitly admitted later administrator")
	}
}

func TestBootstrapExistingUserIsNeverPromotedOrAdmitted(t *testing.T) {
	pool, ctx := bootstrapDatabase(t), context.Background()
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	bootstrapExec(t, pool, "INSERT INTO users(external_subject,display_name,role,password_hash) VALUES('admin','Existing identity','viewer','unchanged-hash')")
	before := bootstrapScalar(t, pool, bootstrapAdmissionSnapshot)
	if err := bootstrapAdmin(ctx, pool, "replacement-hash"); err != nil {
		t.Fatal(err)
	}
	if bootstrapScalar(t, pool, bootstrapAdmissionSnapshot) != before {
		t.Fatal("username conflict promoted/reset/admitted existing identity")
	}
}

func TestBootstrapAdmissionFailureRollsBackInitialIdentity(t *testing.T) {
	pool, ctx := bootstrapDatabase(t), context.Background()
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	bootstrapExec(t, pool, `CREATE FUNCTION reject_bootstrap_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'isolated commit rejection'; END$$;
 CREATE CONSTRAINT TRIGGER reject_bootstrap_audit AFTER INSERT ON legacy_lab_admission_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_bootstrap_audit()`)
	before := bootstrapScalar(t, pool, bootstrapAdmissionSnapshot)
	if err := bootstrapAdmin(ctx, pool, "isolated-hash"); err == nil {
		t.Fatal("expected failed bootstrap commit")
	}
	if bootstrapScalar(t, pool, bootstrapAdmissionSnapshot) != before {
		t.Fatal("failed bootstrap left identity/admission/audit behind")
	}
}
