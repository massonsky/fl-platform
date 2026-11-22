//go:build integration

package transport

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func TestOrganizationSessionExpiresAfterGuardWait(t *testing.T) {
	for _, operation := range []string{"list", "create", "replay", "change"} {
		t.Run(operation, func(t *testing.T) {
			f := authorizationDB(t, true)
			service := organization.Service{Pool: f.pool}
			p := organizationPrincipal(f, "owner")
			id := f.shared(t)
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
			go func() {
				var err error
				switch operation {
				case "list":
					_, err = service.List(ctx, p)
				case "create":
					_, err = service.Create(ctx, p, uuid.NewString(), "Denied while waiting")
				case "replay":
					_, err = service.Create(ctx, p, id, "Authorization fixture")
				case "change":
					_, err = service.ChangeMember(ctx, p, id, p.UserID, organization.MemberChange{Role: "owner", Status: "active", Revision: 1})
				}
				done <- err
			}()
			mode := "ExclusiveLock"
			if operation == "list" {
				mode = "ShareLock"
			}
			waitProjectGuard(t, ctx, f.pool, mode)
			// Transaction began while token was valid. now() would incorrectly accept
			// it after this commit; Current must use wall clock after obtaining guard.
			if _, err = hold.Exec(ctx, "UPDATE sessions SET expires_at=clock_timestamp() WHERE token_hash=$1", p.SessionHash); err != nil {
				t.Fatal(err)
			}
			if err = hold.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			organizationFailure(t, <-done, 401, "UNAUTHORIZED")
			if authorizationValue(t, f.pool, "SELECT count(*)::text FROM organizations") != "1" || authorizationValue(t, f.pool, "SELECT count(*)::text FROM organization_events") != "1" {
				t.Fatal("expired request committed state")
			}
		})
	}
}

func TestOrganizationSessionSQLDeadlineReleasesGuard(t *testing.T) {
	f := authorizationDB(t, true)
	service := organization.Service{Pool: f.pool}
	p := organizationPrincipal(f, "owner")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	// Current is a normal read and succeeds. createTx's actor FOR UPDATE waits
	// here, proving its SQL callback uses the Service's bounded context.
	if _, err = hold.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", p.UserID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() { _, err := service.Create(ctx, p, uuid.NewString(), "Bounded SQL"); done <- err }()
	for {
		var granted bool
		err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory'
 AND objid=726182902 AND mode='ExclusiveLock' AND granted
 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&granted)
		if err != nil {
			t.Fatal(err)
		}
		if granted {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("guard never acquired: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	organizationFailure(t, <-done, 503, "DEPENDENCY_UNAVAILABLE")
	if elapsed := time.Since(start); elapsed < authorization.OperationTimeout-time.Second || elapsed >= 14*time.Second {
		t.Fatalf("SQL ignored operation timeout: %v", elapsed)
	}
	probe, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = f.s.Identity.Logout(probe, p.UserID); err != nil {
		t.Fatal("SQL timeout retained write guard", err)
	}
	if err = hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM organizations") != "0" || authorizationValue(t, f.pool, "SELECT count(*)::text FROM organization_events") != "0" {
		t.Fatal("timed-out create committed state")
	}
}
