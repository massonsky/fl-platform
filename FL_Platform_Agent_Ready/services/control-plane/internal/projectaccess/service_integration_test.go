//go:build integration

package projectaccess

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func servicePrincipal(t *testing.T, pool *pgxpool.Pool, actor string) identity.Principal {
	t.Helper()
	p := identity.Principal{UserID: actor, SessionHash: identity.Hash(uuid.NewString())}
	accessExec(t, pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')", p.SessionHash, actor)
	return p
}
func serviceSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	return accessScalar(t, pool, `SELECT jsonb_build_object(
 'projects',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM projects p),
 'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY project_id,user_id) FROM project_members g),
 'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM project_access_events e))::text`)
}

func TestServiceRejectsInvalidSessionBeforeEveryOperationAndReplay(t *testing.T) {
	pool, _, owner, org, project := accessFixture(t)
	ctx, s := context.Background(), Service{Pool: pool}
	target := accessMember(t, pool, org, "researcher", "researcher")
	valid := servicePrincipal(t, pool, owner)
	foreign := servicePrincipal(t, pool, target)
	expired := servicePrincipal(t, pool, owner)
	accessExec(t, pool, "UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1", expired.SessionHash)
	revoked := servicePrincipal(t, pool, owner)
	accessExec(t, pool, "DELETE FROM sessions WHERE token_hash=$1", revoked.SessionHash)
	cases := map[string]identity.Principal{
		"missing": {}, "malformed": {UserID: owner, SessionHash: "not-a-hash"},
		"foreign": {UserID: owner, SessionHash: foreign.SessionHash}, "expired": expired, "revoked": revoked,
	}
	for name, principal := range cases {
		t.Run(name, func(t *testing.T) {
			before := serviceSnapshot(t, pool)
			p, err := s.Create(ctx, principal, org, project, CreateInput{"fixture", "Fixture"})
			accessFailure(t, err, 401, "UNAUTHORIZED")
			if p.ID != "" {
				t.Fatal("unauthorized replay returned DTO")
			}
			_, err = s.Get(ctx, principal, org, project)
			accessFailure(t, err, 401, "UNAUTHORIZED")
			_, err = s.Page(ctx, principal, org, PageInput{})
			accessFailure(t, err, 401, "UNAUTHORIZED")
			_, err = s.Roster(ctx, principal, org, project, MemberPageInput{})
			accessFailure(t, err, 401, "UNAUTHORIZED")
			_, err = s.Candidates(ctx, principal, org, project, MemberPageInput{})
			accessFailure(t, err, 401, "UNAUTHORIZED")
			_, err = s.SaveGrant(ctx, principal, org, project, target, GrantInput{"researcher", "active", 0})
			accessFailure(t, err, 401, "UNAUTHORIZED")
			_, err = s.SetVisibility(ctx, principal, org, project, VisibilityInput{"organization", 1})
			accessFailure(t, err, 401, "UNAUTHORIZED")
			if serviceSnapshot(t, pool) != before {
				t.Fatal("invalid session changed data/audit")
			}
		})
	}
	accessExec(t, pool, "UPDATE users SET disabled_at=clock_timestamp() WHERE id=$1", owner)
	_, err := s.Create(ctx, valid, org, project, CreateInput{"fixture", "Fixture"})
	accessFailure(t, err, 401, "UNAUTHORIZED")
}

func TestServiceCurrentCapsAndTransactionalReplay(t *testing.T) {
	pool, _, owner, org, project := accessFixture(t)
	ctx, s := context.Background(), Service{Pool: pool}
	p := servicePrincipal(t, pool, owner)
	target := accessMember(t, pool, org, "viewer", "researcher")
	g, err := s.SaveGrant(ctx, p, org, project, target, GrantInput{"researcher", "active", 0})
	if err != nil || g.Revision != 1 {
		t.Fatal(g, err)
	}
	updated, err := s.SetVisibility(ctx, p, org, project, VisibilityInput{"organization", 2})
	if err != nil || updated.ACLRevision != 3 {
		t.Fatal(updated, err)
	}
	before := serviceSnapshot(t, pool)
	replay, err := s.Create(ctx, p, org, project, CreateInput{"fixture", " Fixture "})
	if err != nil || replay.Visibility != "organization" || replay.ACLRevision != 3 || serviceSnapshot(t, pool) != before {
		t.Fatal("replay reset state", replay, err)
	}
	accessExec(t, pool, "UPDATE users SET role='viewer' WHERE id=$1", owner)
	page, err := s.Page(ctx, p, org, PageInput{})
	if err != nil || page.Context.Capabilities.CreateProjects || len(page.Items) != 1 || page.Items[0].Capabilities.ManageAccess {
		t.Fatal("stale global role", page, err)
	}
	_, err = s.SaveGrant(ctx, p, org, project, target, GrantInput{"viewer", "revoked", 1})
	accessFailure(t, err, 403, "FORBIDDEN")
	roster, err := s.Roster(ctx, p, org, project, MemberPageInput{})
	if err != nil || len(roster.Items) != 2 {
		t.Fatal("read-only manager roster", roster, err)
	}
	for _, m := range roster.Items {
		if m.CanGrant || m.CanRevoke {
			t.Fatal("read-only manager can mutate", m)
		}
	}
	accessExec(t, pool, "UPDATE users SET role='researcher' WHERE id=$1", owner)
	accessExec(t, pool, "UPDATE organizations SET state='suspended' WHERE id=$1", org)
	page, err = s.Page(ctx, p, org, PageInput{})
	if err != nil || page.Context.Capabilities.CreateProjects || !page.Items[0].Capabilities.Read {
		t.Fatal(page, err)
	}
	_, err = s.SetVisibility(ctx, p, org, project, VisibilityInput{"private", 3})
	accessFailure(t, err, 403, "FORBIDDEN")
}

func TestServicePartialSchemaAndCommitFailureAreUnavailableWithoutDTO(t *testing.T) {
	pool, _, owner, org, project := accessFixture(t)
	ctx, s := context.Background(), Service{Pool: pool}
	p := servicePrincipal(t, pool, owner)
	accessExec(t, pool, "ALTER TABLE project_access_events RENAME TO unavailable_events")
	got, err := s.Get(ctx, p, org, project)
	accessFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if got.ID != "" {
		t.Fatal("partial schema returned DTO")
	}
	accessExec(t, pool, "ALTER TABLE unavailable_events RENAME TO project_access_events")
	accessExec(t, pool, `CREATE FUNCTION reject_project_commit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'isolated commit failure'; END$$;
 CREATE CONSTRAINT TRIGGER reject_project_commit AFTER INSERT ON projects DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_project_commit()`)
	before := serviceSnapshot(t, pool)
	got, err = s.Create(ctx, p, org, uuid.NewString(), CreateInput{"rollback", "Rollback"})
	accessFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if got.ID != "" || serviceSnapshot(t, pool) != before {
		t.Fatal("commit failure leaked DTO or writes")
	}
	encoded, err := json.Marshal(p)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("principal serialized", string(encoded), err)
	}
}
