//go:build integration

package organization

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func organizationFixture(t *testing.T) (*pgxpool.Pool, Store, string, string) {
	t.Helper()
	pool := organizationDatabase(t, nil)
	s := Store{Pool: pool}
	owner, id := organizationUser(t, pool, "researcher"), uuid.NewString()
	if _, err := s.Create(context.Background(), owner, id, "Membership fixture"); err != nil {
		t.Fatal(err)
	}
	return pool, s, owner, id
}

func fixtureMember(t *testing.T, pool *pgxpool.Pool, organizationID, globalRole, role string) string {
	t.Helper()
	id := organizationUser(t, pool, globalRole)
	organizationExec(t, pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,$3)", organizationID, id, role)
	return id
}

func TestOrganizationMemberAccessRevisionAndRevocation(t *testing.T) {
	pool, s, owner, id := organizationFixture(t)
	ctx := context.Background()
	admin := fixtureMember(t, pool, id, "researcher", "admin")
	researcher := fixtureMember(t, pool, id, "researcher", "researcher")
	viewer := fixtureMember(t, pool, id, "researcher", "viewer")
	legacyViewer := fixtureMember(t, pool, id, "viewer", "owner")
	disabled := fixtureMember(t, pool, id, "admin", "admin")
	organizationExec(t, pool, "UPDATE users SET disabled_at=now() WHERE id=$1", disabled)
	foreign := organizationUser(t, pool, "admin")
	for _, actor := range []string{researcher, viewer, legacyViewer, disabled} {
		_, err := s.ChangeMember(ctx, actor, id, viewer, MemberChange{"researcher", "active", 1})
		requireOrganizationFailure(t, err, 403, "FORBIDDEN")
	}
	_, err := s.ChangeMember(ctx, foreign, id, viewer, MemberChange{"researcher", "active", 1})
	requireOrganizationFailure(t, err, 404, "NOT_FOUND")
	_, err = s.ListMembers(ctx, foreign, id)
	requireOrganizationFailure(t, err, 404, "NOT_FOUND")
	_, err = s.ListMembers(ctx, researcher, id)
	requireOrganizationFailure(t, err, 403, "FORBIDDEN")
	_, err = s.ChangeMember(ctx, admin, id, owner, MemberChange{"viewer", "active", 1})
	requireOrganizationFailure(t, err, 403, "FORBIDDEN")
	_, err = s.ChangeMember(ctx, admin, id, researcher, MemberChange{"owner", "active", 1})
	requireOrganizationFailure(t, err, 403, "FORBIDDEN")
	m, err := s.ChangeMember(ctx, admin, id, viewer, MemberChange{"researcher", "active", 1})
	if err != nil || m.Revision != 2 || m.Role != "researcher" {
		t.Fatalf("valid change %+v: %v", m, err)
	}
	_, err = s.ChangeMember(ctx, owner, id, viewer, MemberChange{"viewer", "suspended", 1})
	requireOrganizationFailure(t, err, 409, "VERSION_CONFLICT")
	_, err = s.ChangeMember(ctx, owner, id, viewer, MemberChange{"researcher", "active", 2})
	if err != nil || organizationScalar(t, pool, "SELECT count(*)::text FROM organization_events WHERE organization_id=$1", id) != "2" {
		t.Fatalf("failed/no-op update emitted audit: %v", err)
	}
	if _, err = s.ChangeMember(ctx, owner, id, viewer, MemberChange{"researcher", "revoked", 2}); err != nil {
		t.Fatal(err)
	}
	items, err := s.List(ctx, viewer)
	if err != nil || len(items) != 0 {
		t.Fatalf("revoked user retained organization: %+v %v", items, err)
	}
	_, err = s.ListMembers(ctx, viewer, id)
	requireOrganizationFailure(t, err, 404, "NOT_FOUND")
	organizationExec(t, pool, "UPDATE organizations SET state='suspended' WHERE id=$1", id)
	items, err = s.List(ctx, owner)
	if err != nil || len(items) != 1 || items[0].CanManageMembers {
		t.Fatalf("suspended organization read/cap: %+v %v", items, err)
	}
	_, err = s.ChangeMember(ctx, owner, id, researcher, MemberChange{"admin", "active", 1})
	requireOrganizationFailure(t, err, 403, "FORBIDDEN")
}

func TestOrganizationLastEffectiveOwnerAndRollback(t *testing.T) {
	pool, s, owner, id := organizationFixture(t)
	fixtureMember(t, pool, id, "viewer", "owner")
	disabled := fixtureMember(t, pool, id, "admin", "owner")
	organizationExec(t, pool, "UPDATE users SET disabled_at=now() WHERE id=$1", disabled)
	for _, change := range []MemberChange{{"viewer", "active", 1}, {"owner", "suspended", 1}, {"owner", "revoked", 1}} {
		_, err := s.ChangeMember(context.Background(), owner, id, owner, change)
		requireOrganizationFailure(t, err, 409, "LAST_OWNER")
	}
	if organizationScalar(t, pool, "SELECT role||':'||status||':'||revision::text FROM organization_memberships WHERE organization_id=$1 AND user_id=$2", id, owner) != "owner:active:1" ||
		organizationScalar(t, pool, "SELECT count(*)::text FROM organization_events WHERE organization_id=$1", id) != "1" ||
		organizationScalar(t, pool, "SELECT revision::text FROM organizations WHERE id=$1", id) != "1" {
		t.Fatal("last-owner refusal changed member, organization or audit")
	}
}

func TestOrganizationConcurrentOwnerDemotion(t *testing.T) {
	pool, s, first, id := organizationFixture(t)
	second := fixtureMember(t, pool, id, "researcher", "owner")
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, actor := range []string{first, second} {
		workers.Add(1)
		go func(actor string) {
			defer workers.Done()
			<-start
			_, err := s.ChangeMember(context.Background(), actor, id, actor, MemberChange{"researcher", "active", 1})
			results <- err
		}(actor)
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded, denied := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			var failure Failure
			if !errors.As(err, &failure) || failure.Code != "LAST_OWNER" {
				t.Fatalf("unexpected concurrent error: %v", err)
			}
			denied++
		}
	}
	if succeeded != 1 || denied != 1 ||
		organizationScalar(t, pool, "SELECT count(*)::text FROM organization_memberships WHERE organization_id=$1 AND role='owner' AND status='active'", id) != "1" ||
		organizationScalar(t, pool, "SELECT count(*)::text FROM organization_events WHERE organization_id=$1", id) != "2" {
		t.Fatalf("owner race results: success=%d denied=%d", succeeded, denied)
	}
}

func TestOrganizationConcurrentCrossOwnerChanges(t *testing.T) {
	pool, s, first, id := organizationFixture(t)
	second := fixtureMember(t, pool, id, "researcher", "owner")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, pair := range [][2]string{{first, second}, {second, first}} {
		go func(actor, target string) {
			<-start
			_, err := s.ChangeMember(context.Background(), actor, id, target, MemberChange{"researcher", "active", 1})
			results <- err
		}(pair[0], pair[1])
	}
	close(start)
	succeeded, denied := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			succeeded++
		} else {
			var failure Failure
			if !errors.As(err, &failure) || failure.Code != "FORBIDDEN" {
				t.Fatalf("cross-owner mutation failed without serialized authorization: %v", err)
			}
			denied++
		}
	}
	if succeeded != 1 || denied != 1 ||
		organizationScalar(t, pool, "SELECT count(*)::text FROM organization_memberships WHERE organization_id=$1 AND role='owner' AND status='active'", id) != "1" {
		t.Fatalf("cross-owner results: success=%d denied=%d", succeeded, denied)
	}
}
