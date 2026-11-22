//go:build integration

package transport

import (
	"context"
	"testing"
	"time"

	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func TestAdministrationSessionExpiryAfterGuardWait(t *testing.T) {
	for _, mode := range []string{"page", "mutation"} {
		t.Run(mode, func(t *testing.T) {
			f := authorizationDB(t, false)
			s := admin.Service{Pool: f.pool}
			if mode == "mutation" {
				s = configureCredentialAdministration(t, f.s)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p := adminSessionPrincipal(t, f, f.users["admin"].ID)
			authorizationExec(t, f.pool, "UPDATE sessions SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE token_hash=$1", p.SessionHash)
			hold, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Rollback(context.Background())
			if err = authorization.Write(ctx, hold); err != nil {
				t.Fatal(err)
			}
			before := adminSessionSnapshot(t, f)
			done := make(chan error, 1)
			go func() {
				if mode == "page" {
					_, _, err := s.Page(ctx, p, "users", 0, 10)
					done <- err
					return
				}
				_, err := s.MutateCredential(ctx, p, administrationCredentialCommand("user.create", "", "expired-admin-key", "expired-fingerprint", admin.Input{Username: "expired-admin", Name: "Expired", Role: "viewer", Password: "valid-new-password"}))
				done <- err
			}()
			// Credential work now starts with a short reader preflight. We still
			// observe the blocked guard before expiry, then require401/no effects.
			waitProjectGuard(t, ctx, f.pool, "ShareLock")
			for {
				var expired bool
				if err = f.pool.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM sessions WHERE token_hash=$1", p.SessionHash).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err = hold.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			adminSessionFailure(t, <-done, 401, "UNAUTHORIZED")
			if adminSessionSnapshot(t, f) != before {
				t.Fatal("expired waiter changed state")
			}
		})
	}
}

func TestAdministrationHelperSQLDeadlineReleasesWriterGuard(t *testing.T) {
	f := authorizationDB(t, false)
	s := configureCredentialAdministration(t, f.s)
	p := f.users["admin"].Principal()
	authorizationExec(t, f.pool, `CREATE FUNCTION slow_admin_insert() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_sleep(15); RETURN NEW; END$$;
 CREATE TRIGGER slow_admin_insert BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION slow_admin_insert()`)
	before := adminSessionSnapshot(t, f)
	started := time.Now()
	got, err := s.MutateCredential(context.Background(), p, administrationCredentialCommand("user.create", "", "slow-admin-key", "slow-fingerprint", admin.Input{Username: "slow-admin", Name: "Slow", Role: "viewer", Password: "valid-new-password"}))
	adminSessionFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if time.Since(started) > authorization.OperationTimeout+2*time.Second || got != nil || adminSessionSnapshot(t, f) != before {
		t.Fatal("helper ignored deadline or committed partial data")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = f.s.Identity.Logout(ctx, f.users["admin"].ID); err != nil {
		t.Fatal("expired mutation retained writer guard", err)
	}
}

func TestAdministrationPageDeadlineReleasesReaderGuard(t *testing.T) {
	f := authorizationDB(t, false)
	s := admin.Service{Pool: f.pool}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if _, err = hold.Exec(ctx, "LOCK TABLE administration_events IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	items, more, err := s.Page(ctx, f.users["admin"].Principal(), "events", 0, 10)
	adminSessionFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if time.Since(started) > authorization.OperationTimeout+2*time.Second || items != nil || more {
		t.Fatal("page helper ignored deadline or returned partial rows")
	}
	probe, cancelProbe := context.WithTimeout(context.Background(), time.Second)
	defer cancelProbe()
	if err = f.s.Identity.Logout(probe, f.users["admin"].ID); err != nil {
		t.Fatal("expired page retained reader guard", err)
	}
}
