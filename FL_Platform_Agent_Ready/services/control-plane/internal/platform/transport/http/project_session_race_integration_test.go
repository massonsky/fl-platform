//go:build integration

package transport

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func waitProjectGuard(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mode string) {
	t.Helper()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory'
 AND objid=726182902 AND mode=$1 AND NOT granted
 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`, mode).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestProjectSessionExpiresWhileWaitingForGuard(t *testing.T) {
	f := projectFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := f.users["owner"]
	access := experiment.Access{Principal: u.Principal()}
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
		_, err := f.s.Experiments.List(ctx, access, "projects", "", "", time.Now(), 0, 10)
		done <- err
	}()
	waitProjectGuard(t, ctx, f.pool, "ShareLock")
	// The protected transaction began before this timestamp. transaction now()
	// would accept the session after release; clock_timestamp() must reject it.
	if _, err = hold.Exec(ctx, "UPDATE sessions SET expires_at=clock_timestamp() WHERE token_hash=$1", u.SessionHash); err != nil {
		t.Fatal(err)
	}
	if err = hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	projectFailure(t, <-done, 401, "UNAUTHORIZED")
}

func TestProjectSessionLogoutSerializesWithProtectedRead(t *testing.T) {
	f := projectFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := f.users["owner"]
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if err = authorization.Read(ctx, hold); err != nil {
		t.Fatal(err)
	}
	if _, err = identity.Current(ctx, hold, u.Principal()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.s.Identity.Logout(ctx, u.ID) }()
	waitProjectGuard(t, ctx, f.pool, "ExclusiveLock")
	if _, err = identity.Current(ctx, hold, u.Principal()); err != nil {
		t.Fatal("logout changed protected read before commit", err)
	}
	if err = hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Experiments.List(ctx, experiment.Access{Principal: u.Principal()}, "projects", "", "", time.Now(), 0, 10)
	projectFailure(t, err, 401, "UNAUTHORIZED")
	// Other users' sessions are independent of this logout.
	if _, err = f.s.Experiments.List(ctx, experiment.Access{Principal: f.users["other"].Principal()}, "projects", "", "", time.Now(), 0, 10); err != nil {
		t.Fatal(err)
	}
}

func TestProjectSessionLegacySchemaEightAndFailureClassification(t *testing.T) {
	f := projectFixture(t, true)
	ctx := context.Background()
	u := f.users["owner"]
	access := experiment.Access{Principal: u.Principal()}
	id := uuid.NewString()
	org := f.shared(t)
	authorizationExec(t, f.pool, "UPDATE organizations SET kind='personal',personal_owner_id=$2,state='suspended' WHERE id=$1", org, u.ID)
	authorizationExec(t, f.pool, "UPDATE organization_memberships SET status='revoked' WHERE organization_id=$1", org)
	authorizationExec(t, f.pool, "INSERT INTO projects(id,owner_id,organization_id,slug,name) VALUES($1,$2,$3,'legacy-eight','Legacy')", id, u.ID, org)
	e, r := projectResources(t, f, id, u.ID)
	if _, err := f.s.Experiments.Detail(ctx, access, "runs", r); err != nil {
		t.Fatal("personal metadata must not revoke legacy owner", err)
	}
	_, err := f.s.Experiments.Mutate(ctx, access, "queue", e, "legacy-eight-queue", nil)
	projectFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	projectHTTP(t, f, "owner", "POST", "projects", `{"slug":"legacy-post-eight","name":"Legacy after migration"}`, 503, "DEPENDENCY_UNAVAILABLE")
	// Admission migration is explicit; suspended personal metadata still cannot
	// replace legacy owner authorization after this independent grant backfill.
	legacyWorkSchema(t, f, 8)
	if _, err := f.s.Experiments.Mutate(ctx, access, "queue", e, "legacy-eight-queue", nil); err != nil {
		t.Fatal(err)
	}
	projectHTTP(t, f, "owner", "POST", "projects", `{"slug":"legacy-post-eight","name":"Legacy after migration"}`, 201, "")
	// /me already authenticated this user; a later SQL resource error is 503,
	// not a false 404, and must not disclose SQL text through the HTTP mapper.
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = "pg_catalog"
	broken, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	err = experiment.Scope(ctx, broken, u.ID, "runs", r)
	projectFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	err = experiment.Scope(ctx, f.pool, u.ID, "runs", uuid.NewString())
	projectFailure(t, err, 404, "NOT_FOUND")
	authorizationExec(t, f.pool, "ALTER TABLE project_members RENAME TO unavailable_project_members")
	_, err = f.s.Experiments.Detail(ctx, access, "runs", r)
	projectFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	_, err = f.s.Experiments.Mutate(ctx, access, "queue", e, "legacy-eight-queue", nil)
	projectFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	projectHTTP(t, f, "owner", "GET", "runs/"+r, "", 503, "DEPENDENCY_UNAVAILABLE")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM http_receipts WHERE operation='queue'") != "1" {
		t.Fatal("partial schema replay created work")
	}
}

func TestProjectSessionDeadlineReleasesGuard(t *testing.T) {
	f := legacyWorkFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if _, err = hold.Exec(ctx, "LOCK TABLE projects IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := f.s.Experiments.Mutate(ctx, experiment.Access{Principal: f.users["owner"].Principal()}, "project", "", "bounded-project-key", []byte(`{"slug":"bounded-project","name":"Bounded"}`))
		done <- err
	}()
	// Confirm the actual request holds Read while waiting for a project row/table
	// dependency. Its own 10s deadline, rather than the caller's 15s, must release it.
	for {
		var held bool
		err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory'
 AND objid=726182902 AND mode='ShareLock' AND granted
 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&held)
		if err != nil {
			t.Fatal(err)
		}
		if held {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("operation never held guard: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	projectFailure(t, <-done, 503, "DEPENDENCY_UNAVAILABLE")
	if elapsed := time.Since(started); elapsed < authorization.OperationTimeout-time.Second || elapsed >= 14*time.Second {
		t.Fatalf("unexpected operation deadline %v", elapsed)
	}
	probeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = f.s.Identity.Logout(probeCtx, f.users["owner"].ID); err != nil {
		t.Fatal("timed-out operation retained authorization guard", err)
	}
	if err = hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM projects") != "0" || authorizationValue(t, f.pool, "SELECT count(*)::text FROM http_receipts") != "0" {
		t.Fatal("timed-out write committed partial state")
	}
}
