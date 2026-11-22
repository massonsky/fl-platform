//go:build integration

package projectaccess

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestAccessGrantPrecedenceCASAndAudit(t *testing.T) {
	pool, s, creator, org, id := accessFixture(t)
	ctx := context.Background()
	ref := ResourceRef{Kind: "project", ID: id, OrganizationID: org}
	owner := accessMember(t, pool, org, "researcher", "owner")
	accessExec(t, pool, "UPDATE organization_memberships SET role='researcher' WHERE organization_id=$1 AND user_id=$2", org, creator)
	p, err := s.Get(ctx, creator, ref)
	if err != nil || !p.Capabilities.CreateExperiment || p.Capabilities.ManageAccess {
		t.Fatal("creator baseline", p, err)
	}
	_, err = s.Get(ctx, creator, ResourceRef{Kind: "project", ID: id})
	accessFailure(t, err, 404, "NOT_FOUND")
	g, err := s.SaveGrant(ctx, owner, org, id, creator, GrantInput{"viewer", "active", 0})
	if err != nil || g.Revision != 1 {
		t.Fatal(g, err)
	}
	p, err = s.Get(ctx, creator, ref)
	if err != nil || p.Capabilities.CreateExperiment || p.AccessSource != "explicit_viewer" {
		t.Fatal("viewer did not cap creator", p, err)
	}
	_, err = s.SaveGrant(ctx, owner, org, id, creator, GrantInput{"researcher", "active", 0})
	accessFailure(t, err, 409, "VERSION_CONFLICT")
	if _, err = s.SaveGrant(ctx, owner, org, id, creator, GrantInput{"viewer", "active", 1}); err != nil {
		t.Fatal(err)
	}
	if accessScalar(t, pool, "SELECT count(*)::text FROM project_access_events WHERE project_id=$1", id) != "2" {
		t.Fatal("stale/no-op grant emitted audit")
	}
	p, err = s.SetVisibility(ctx, owner, org, id, VisibilityInput{"organization", 2})
	if err != nil || p.ACLRevision != 3 {
		t.Fatal(p, err)
	}
	_, err = s.SetVisibility(ctx, owner, org, id, VisibilityInput{"private", 2})
	accessFailure(t, err, 409, "VERSION_CONFLICT")
	ordinary := accessMember(t, pool, org, "researcher", "researcher")
	p, err = s.Get(ctx, ordinary, ref)
	if err != nil || !p.Capabilities.Read || p.Capabilities.CreateExperiment {
		t.Fatal("organization visibility must only read", p, err)
	}
	_, err = s.SaveGrant(ctx, owner, org, id, creator, GrantInput{"viewer", "revoked", 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ctx, creator, ref)
	accessFailure(t, err, 404, "NOT_FOUND")
	items, err := s.List(ctx, creator, org, 100)
	if err != nil || len(items) != 0 {
		t.Fatal("explicit deny leaked through list", items, err)
	}
	_, err = s.SaveGrant(ctx, owner, org, id, ordinary, GrantInput{"viewer", "revoked", 0})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ctx, ordinary, ref)
	accessFailure(t, err, 404, "NOT_FOUND")
	_, err = s.SaveGrant(ctx, owner, org, id, owner, GrantInput{"viewer", "revoked", 0})
	accessFailure(t, err, 409, "INHERITED_ACCESS")
	accessExec(t, pool, "UPDATE organizations SET state='suspended' WHERE id=$1", org)
	p, err = s.Get(ctx, owner, ref)
	if err != nil || !p.Capabilities.Export || p.Capabilities.ManageAccess {
		t.Fatal("suspended org cap", p, err)
	}
	_, err = s.SetVisibility(ctx, owner, org, id, VisibilityInput{"private", p.ACLRevision})
	accessFailure(t, err, 403, "FORBIDDEN")
}

func TestAccessResourceContextCapsAndQueueGate(t *testing.T) {
	pool, s, owner, org, id := accessFixture(t)
	ctx := context.Background()
	member := accessMember(t, pool, org, "viewer", "researcher")
	if _, err := s.SaveGrant(ctx, owner, org, id, member, GrantInput{"researcher", "active", 0}); err != nil {
		t.Fatal(err)
	}
	otherOrg := accessOrg(t, pool, owner)
	_, err := s.Get(ctx, owner, ResourceRef{Kind: "project", ID: id, OrganizationID: otherOrg})
	accessFailure(t, err, 404, "NOT_FOUND")
	experiment, run := uuid.NewString(), uuid.NewString()
	accessExec(t, pool, "INSERT INTO experiments(id,project_id,name,config_json,config_hash) VALUES($1,$2,'Fixture','{}','fixture')", experiment, id)
	accessExec(t, pool, "INSERT INTO runs(id,experiment_id,created_by,status,manifest_json,manifest_hash) VALUES($1,$2,$3,'draft','{}','fixture')", run, experiment, owner)
	for _, ref := range []ResourceRef{{"experiment", experiment, org}, {"run", run, org}} {
		p, err := s.Get(ctx, member, ref)
		if err != nil || p.ID != id || !p.Capabilities.Read || p.Capabilities.CreateExperiment {
			t.Fatal("resource/global viewer cap", p, err)
		}
	}
	called := false
	err = s.Within(ctx, owner, ResourceRef{"experiment", experiment, org}, QueueRun, func(pgx.Tx, Project) error { called = true; return nil })
	accessFailure(t, err, 503, "ORG_AGENTS_NOT_READY")
	if called || accessScalar(t, pool, "SELECT count(*)::text FROM runs") != "1" || accessScalar(t, pool, "SELECT count(*)::text FROM tasks") != "0" {
		t.Fatal("shared queue reached execution")
	}
	accessExec(t, pool, "UPDATE organization_memberships SET status='revoked' WHERE organization_id=$1 AND user_id=$2", org, member)
	_, err = s.Get(ctx, member, ResourceRef{"run", run, org})
	accessFailure(t, err, 404, "NOT_FOUND")
}

func TestAccessRevocationWaitsForProtectedWorkAndThenDenies(t *testing.T) {
	pool, s, owner, org, id := accessFixture(t)
	member := accessMember(t, pool, org, "researcher", "researcher")
	if _, err := s.SaveGrant(context.Background(), owner, org, id, member, GrantInput{"researcher", "active", 0}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	protected, revoked := make(chan error, 1), make(chan error, 1)
	go func() {
		protected <- s.Within(ctx, member, ResourceRef{"project", id, org}, CreateExperiment, func(pgx.Tx, Project) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		_, err := s.SaveGrant(ctx, owner, org, id, member, GrantInput{"researcher", "revoked", 1})
		revoked <- err
	}()
	blocked := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted
 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			blocked = true
			break
		}
		select {
		case err := <-revoked:
			t.Fatalf("revocation crossed active permission barrier: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("revocation did not wait on authorization guard")
	}
	once.Do(func() { close(release) })
	if err := <-protected; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	called := false
	err := s.Within(ctx, member, ResourceRef{"project", id, org}, CreateExperiment, func(pgx.Tx, Project) error { called = true; return nil })
	accessFailure(t, err, 404, "NOT_FOUND")
	if called {
		t.Fatal("revoked request executed callback")
	}
}
