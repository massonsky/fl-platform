//go:build integration

package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func TestAuthorizationCrossDomainOwnerRace(t *testing.T) {
	f := authorizationDB(t, true)
	id, first, second := f.shared(t), f.users["owner"].ID, f.users["other"].ID
	authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", id, second)
	for i := 0; i < 6; i++ {
		authorizationExec(t, f.pool, "UPDATE users SET disabled_at=NULL WHERE id=$1", first)
		authorizationExec(t, f.pool, "UPDATE organization_memberships SET role='owner',revision=1 WHERE organization_id=$1", id)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start, results := make(chan struct{}), make(chan error, 2)
		go func() {
			<-start
			disabled := true
			_, err := (admin.Store{Pool: f.pool}).Mutate(ctx, f.users["admin"].ID, "user.update", first, uuid.NewString(), "race-disable", admin.Input{Role: "researcher", Disabled: &disabled})
			results <- err
		}()
		go func() {
			<-start
			_, err := (organization.Store{Pool: f.pool}).ChangeMember(ctx, second, id, second, organization.MemberChange{Role: "researcher", Status: "active", Revision: 1})
			results <- err
		}()
		close(start)
		succeeded, denied := 0, 0
		for n := 0; n < 2; n++ {
			err := <-results
			if err == nil {
				succeeded++
				continue
			}
			var failure organization.Failure
			if !errors.As(err, &failure) || failure.Code != "LAST_OWNER" {
				cancel()
				t.Fatalf("concurrent change failed unexpectedly (including deadlock): %v", err)
			}
			denied++
		}
		cancel()
		owners := authorizationValue(t, f.pool, `SELECT count(*)::text FROM organization_memberships m JOIN users u ON u.id=m.user_id
 WHERE m.organization_id=$1 AND m.role='owner' AND m.status='active' AND u.disabled_at IS NULL AND u.role IN ('admin','researcher')`, id)
		if succeeded != 1 || denied != 1 || owners != "1" {
			t.Fatalf("race: success=%d last_owner=%d owners=%s", succeeded, denied, owners)
		}
	}
}

func TestAuthorizationGuardBeforeRowLocks(t *testing.T) {
	f := authorizationDB(t, true)
	id, owner, other := f.shared(t), f.users["owner"].ID, f.users["other"].ID
	authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'researcher')", id, other)
	orgs, admins := organization.Store{Pool: f.pool}, admin.Store{Pool: f.pool}
	operations := []struct {
		name, mode string
		run        func(context.Context) error
	}{
		{"create", "ExclusiveLock", func(ctx context.Context) error {
			_, err := orgs.Create(ctx, other, uuid.NewString(), "Guard fixture")
			return err
		}},
		{"member", "ExclusiveLock", func(ctx context.Context) error {
			_, err := orgs.ChangeMember(ctx, owner, id, other, organization.MemberChange{Role: "viewer", Status: "active", Revision: 1})
			return err
		}},
		{"members", "ShareLock", func(ctx context.Context) error { _, err := orgs.ListMembers(ctx, owner, id); return err }},
		{"admin-read", "ShareLock", func(ctx context.Context) error {
			_, _, err := admins.Page(ctx, f.users["admin"].ID, "users", 0, 10)
			return err
		}},
		{"admin-write", "ExclusiveLock", func(ctx context.Context) error {
			disabled := false
			_, err := admins.Mutate(ctx, f.users["admin"].ID, "user.update", other, uuid.NewString(), "guard-change", admin.Input{Role: "researcher", Disabled: &disabled})
			return err
		}},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			hold, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Rollback(context.Background())
			if err = authorization.Write(ctx, hold); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- op.run(ctx) }()
			// Inspect PostgreSQL's actual wait; no scheduling assumptions or sleeps
			// determine whether row locks preceded the authorization guard.
			for {
				var mode string
				err = f.pool.QueryRow(ctx, `SELECT coalesce(max(mode),'') FROM pg_locks WHERE locktype='advisory'
 AND objid=726182902 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&mode)
				if err != nil {
					t.Fatal(err)
				}
				if mode != "" {
					if mode != op.mode {
						t.Fatalf("guard mode %s, want %s", mode, op.mode)
					}
					break
				}
				select {
				case err := <-done:
					t.Fatalf("operation did not wait for guard: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			probe, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Rollback(context.Background())
			if _, err = probe.Exec(ctx, "SELECT id FROM users ORDER BY id FOR UPDATE NOWAIT"); err != nil {
				t.Fatalf("user row locked before authorization guard: %v", err)
			}
			if _, err = probe.Exec(ctx, "SELECT id FROM organizations ORDER BY id FOR UPDATE NOWAIT"); err != nil {
				t.Fatalf("organization row locked before authorization guard: %v", err)
			}
			if err = probe.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = hold.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthorizationIdentityDatabaseErrors(t *testing.T) {
	f := authorizationDB(t, false)
	ctx := context.Background()
	for _, actor := range []identity.User{f.users["disabled"], {ID: uuid.NewString(), Role: "admin"}} {
		actor.Role = "admin" // Stale claim must not override disabled/missing state.
		authorizationHTTP(t, f.s, actor, "admin/users", "", 401, "UNAUTHORIZED")
		authorizationHTTP(t, f.s, actor, "admin/users/"+f.users["other"].ID, `{"role":"viewer","disabled":false}`, 401, "UNAUTHORIZED")
	}
	viewer := f.users["viewer"]
	viewer.Role = "admin"
	authorizationHTTP(t, f.s, viewer, "admin/users", "", 403, "FORBIDDEN")
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = "pg_catalog"
	unavailable, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer unavailable.Close()
	// A real PostgreSQL undefined-table error after Begin/guard, rather than a
	// mock authentication failure. The fixture database and public tables remain.
	broken := &Server{Identity: identity.Store{Pool: unavailable}, CursorKey: f.s.CursorKey}
	authorizationHTTP(t, broken, f.users["admin"], "admin/users", "", 503, "DEPENDENCY_UNAVAILABLE")
	authorizationHTTP(t, broken, f.users["admin"], "admin/users/"+f.users["other"].ID, `{"role":"viewer","disabled":false}`, 503, "DEPENDENCY_UNAVAILABLE")
	authorizationHTTP(t, f.s, f.users["admin"], "admin/users", "", 200, "")
}

func TestAuthorizationPartialOrganizationSchemaFailsClosed(t *testing.T) {
	f := authorizationDB(t, true)
	f.shared(t)
	authorizationExec(t, f.pool, "ALTER TABLE organization_memberships RENAME TO unavailable_memberships")
	// The helper also asserts retryable=true, Retry-After:3 and no auth challenge.
	authorizationHTTP(t, f.s, f.users["admin"], "admin/users/"+f.users["owner"].ID, `{"role":"viewer","disabled":false}`, 503, "DEPENDENCY_UNAVAILABLE")
	if authorizationValue(t, f.pool, "SELECT role FROM users WHERE id=$1", f.users["owner"].ID) != "researcher" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_receipts") != "0" {
		t.Fatal("partial schema was treated as no organizations")
	}
}
