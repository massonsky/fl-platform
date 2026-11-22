//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/legacylab"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func waitLegacyReceipt(t *testing.T, ctx context.Context, f authorizationFixture, key string) {
	t.Helper()
	for {
		var waiting bool
		err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory'
 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND NOT granted
 AND objid=(hashtextextended($1,0)&4294967295)::oid
 AND classid=((hashtextextended($1,0)>>32)&4294967295)::oid)`, key).Scan(&waiting)
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

func TestLegacyWorkQueueBeforeRevokePreservesAcceptedRun(t *testing.T) {
	f := legacyWorkFixture(t)
	_, e, old := legacyWorkResource(t, f, f.users["owner"].ID)
	u := f.users["owner"]
	access := experiment.Access{Principal: u.Principal()}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	key := "ordered-legacy-queue"
	lock := u.ID + "/queue/" + key
	if _, err = hold.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", lock); err != nil {
		t.Fatal(err)
	}
	type result struct {
		value experiment.Result
		err   error
	}
	queued := make(chan result, 1)
	go func() {
		value, mutationErr := f.s.Experiments.Mutate(ctx, access, "queue", e, key, nil)
		queued <- result{value, mutationErr}
	}()
	waitLegacyReceipt(t, ctx, f, lock)
	revoked := make(chan error, 1)
	go func() {
		_, err := (legacylab.Service{Pool: f.pool}).Change(ctx, f.users["admin"].Principal(), u.ID,
			legacylab.Change{Status: "revoked", Revision: 1, Reason: "Revoke after admitted queue"})
		revoked <- err
	}()
	waitProjectGuard(t, ctx, f.pool, "ExclusiveLock")
	before := authorizationValue(t, f.pool, "SELECT manifest_json::text||manifest_hash||status FROM runs WHERE id=$1", old)
	if err = hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-queued
	if got.err != nil || got.value.Status != 202 {
		t.Fatal("accepted queue failed", got.err)
	}
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	var run struct{ ID string }
	if json.Unmarshal(got.value.Body, &run) != nil || run.ID == "" {
		t.Fatal("missing accepted run")
	}
	if authorizationValue(t, f.pool, "SELECT manifest_json::text||manifest_hash||status FROM runs WHERE id=$1", old) != before ||
		authorizationValue(t, f.pool, "SELECT status FROM runs WHERE id=$1", run.ID) != "queued" ||
		authorizationValue(t, f.pool, `SELECT (r.manifest_json=e.config_json AND r.manifest_hash=e.config_hash)::text
 FROM runs r JOIN experiments e ON e.id=r.experiment_id WHERE r.id=$1`, run.ID) != "true" {
		t.Fatal("revocation rewrote accepted run or manifest")
	}
	_, err = f.s.Experiments.Mutate(ctx, access, "queue", e, key, nil)
	projectFailure(t, err, 403, "LEGACY_LAB_NOT_ADMITTED")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM http_receipts WHERE operation='queue'") != "1" {
		t.Fatal("revoked replay changed receipt")
	}
}

func TestLegacyWorkRevokeBeforeQueueDeniesWithoutReceipt(t *testing.T) {
	f := legacyWorkFixture(t)
	_, e, _ := legacyWorkResource(t, f, f.users["owner"].ID)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if err = authorization.Write(ctx, hold); err != nil {
		t.Fatal(err)
	}
	revoked := make(chan error, 1)
	go func() {
		_, err := (legacylab.Service{Pool: f.pool}).Change(ctx, f.users["admin"].Principal(), f.users["owner"].ID,
			legacylab.Change{Status: "revoked", Revision: 1, Reason: "Revoke before waiting queue"})
		revoked <- err
	}()
	waitProjectGuard(t, ctx, f.pool, "ExclusiveLock")
	queued := make(chan error, 1)
	go func() {
		_, err := f.s.Experiments.Mutate(ctx, experiment.Access{Principal: f.users["owner"].Principal()}, "queue", e, "after-revoke-queue", nil)
		queued <- err
	}()
	waitProjectGuard(t, ctx, f.pool, "ShareLock")
	before := legacyWorkCounts(t, f)
	if err = hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	projectFailure(t, <-queued, 403, "LEGACY_LAB_NOT_ADMITTED")
	if legacyWorkCounts(t, f) != before {
		t.Fatal("queue after revoke created work/receipt")
	}
}
