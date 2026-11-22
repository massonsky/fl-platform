//go:build integration

package transport

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

func TestMembershipProjectConcurrentGrantAndRevoke(t *testing.T) {
	f := newMembershipProjects(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if err = authorization.Read(ctx, hold); err != nil {
		t.Fatal(err)
	}
	grant, revoked := make(chan error, 1), make(chan error, 1)
	go func() {
		_, e := (projectaccess.Store{Pool: f.pool}).SaveGrant(ctx, f.users["owner"].ID, f.org, f.projects["race"], f.users["other"].ID, projectaccess.GrantInput{Role: "researcher", Status: "active", Revision: 0})
		grant <- e
	}()
	go func() {
		_, e := (organization.Service{Pool: f.pool}).ChangeMember(ctx, organizationPrincipal(f.authorizationFixture, "owner"), f.org, f.users["other"].ID, organization.MemberChange{Role: "researcher", Status: "revoked", Revision: f.membership.Revision})
		revoked <- e
	}()
	for {
		var count int
		if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND objid=726182902 AND mode='ExclusiveLock' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err = hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-revoked; err != nil {
		t.Fatal("membership revoke", err)
	}
	if err = <-grant; err != nil {
		if f, ok := err.(projectaccess.Failure); !ok || f.Code != "MEMBER_NOT_ACTIVE" {
			t.Fatal("unexpected grant outcome", err)
		}
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM project_members WHERE organization_id=$1 AND user_id=$2 AND status='active'", f.org, f.users["other"].ID) != "0" {
		t.Fatal("race retained active project grant")
	}
}

func TestMembershipProjectSchemaCompatibility(t *testing.T) {
	legacy := authorizationDB(t, true)
	org := legacy.shared(t)
	authorizationExec(t, legacy.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'researcher')", org, legacy.users["other"].ID)
	m, err := (organization.Service{Pool: legacy.pool}).ChangeMember(context.Background(), organizationPrincipal(legacy, "owner"), org, legacy.users["other"].ID, organization.MemberChange{Role: "researcher", Status: "revoked", Revision: 1})
	if err != nil || m.Status != "revoked" || m.Revision != 2 {
		t.Fatal("schema7 compatibility", m, err)
	}
	f := newMembershipProjects(t)
	authorizationExec(t, f.pool, "ALTER TABLE project_access_events RENAME TO absent_project_events")
	_, err = (organization.Service{Pool: f.pool}).ChangeMember(context.Background(), organizationPrincipal(f.authorizationFixture, "owner"), f.org, f.users["other"].ID, organization.MemberChange{Role: "researcher", Status: "revoked", Revision: f.membership.Revision})
	organizationFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if f.grantState(t, "explicit") != "researcher/active/1" || authorizationValue(t, f.pool, "SELECT status FROM organization_memberships WHERE organization_id=$1 AND user_id=$2", f.org, f.users["other"].ID) != "active" {
		t.Fatal("partial schema committed revocation")
	}
}

func TestMembershipProjectDeadlineRollsBackCascade(t *testing.T) {
	f := newMembershipProjects(t)
	authorizationExec(t, f.pool, `CREATE FUNCTION delay_cascade_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.after_json->>'reason'='membership_revoked' THEN PERFORM pg_sleep(15); END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER delay_cascade_event BEFORE INSERT ON project_access_events FOR EACH ROW EXECUTE FUNCTION delay_cascade_event()`)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	_, err := (organization.Service{Pool: f.pool}).ChangeMember(ctx, organizationPrincipal(f.authorizationFixture, "owner"), f.org, f.users["other"].ID, organization.MemberChange{Role: "researcher", Status: "revoked", Revision: f.membership.Revision})
	organizationFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if elapsed := time.Since(start); elapsed < authorization.OperationTimeout-time.Second || elapsed >= 14*time.Second {
		t.Fatalf("cascade timeout %v", elapsed)
	}
	probe, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = f.s.Identity.Logout(probe, f.users["owner"].ID); err != nil {
		t.Fatal("cascade retained write guard", err)
	}
	if f.grantState(t, "explicit") != "researcher/active/1" || f.grantState(t, "creator") != "absent" || authorizationValue(t, f.pool, "SELECT status FROM organization_memberships WHERE organization_id=$1 AND user_id=$2", f.org, f.users["other"].ID) != "active" {
		t.Fatal("timeout committed partial revocation")
	}
}

func TestMembershipProjectRevokeWhileExportLoads(t *testing.T) {
	f := newExportFixture(t, true)
	entered, resume := make(chan struct{}), make(chan struct{})
	defer close(resume)
	f.s.Experiments.Blobs = exportBlobFunc(func(ctx context.Context, _ string, _ string, _ int64) ([]byte, error) {
		close(entered)
		select {
		case <-resume:
			return f.model, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); f.s.export(w, f.request(), f.access, f.run) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("export snapshot did not reach S3")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := (organization.Service{Pool: f.pool}).ChangeMember(ctx, organizationPrincipal(f.authorizationFixture, "other"), f.org, f.users["owner"].ID, organization.MemberChange{Role: "researcher", Status: "revoked", Revision: 1})
	if err != nil {
		t.Fatal("S3 held authorization guard", err)
	}
	resume <- struct{}{}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("export did not finish")
	}
	if w.Code != 404 || w.Header().Get("Content-Type") != "application/json" || f.s.exportBusy.Load() {
		t.Fatal("membership revoked export was delivered", w.Code)
	}
	if authorizationValue(t, f.pool, "SELECT status FROM project_members WHERE project_id=$1 AND user_id=$2", f.project, f.users["owner"].ID) != "revoked" {
		t.Fatal("creator baseline was not tombstoned")
	}
}
