//go:build integration

package credentials

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

func TestCapacityFullKeepsExistingKeysAndOtherKindsWorking(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	ctx := context.Background()
	if err := s.Client(ctx, netip.MustParseAddr("192.0.2.2"), 1); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `INSERT INTO credential_limit_buckets(kind,digest,tat,expires_at)
 SELECT 'client',decode(lpad(to_hex(n),64,'0'),'hex'),clock_timestamp(),clock_timestamp()+interval '30 minutes'
 FROM generate_series(1,16383) n;
 UPDATE credential_limit_capacity SET used=16384 WHERE kind='client'`)
	failureIs(t, s.Client(ctx, netip.MustParseAddr("192.0.2.3"), 1), 503)
	if err := s.Client(ctx, netip.MustParseAddr("192.0.2.2"), 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Account(ctx, "separate-kind"); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, pool)
}

func TestCollectorBatchRetentionAndSharedSchedule(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	ctx := context.Background()
	if err := s.Account(ctx, "keep-live"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `INSERT INTO credential_limit_buckets(kind,digest,tat,expires_at)
 SELECT 'client',decode(lpad(to_hex(n),64,'0'),'hex'),clock_timestamp()-interval '40 minutes',clock_timestamp()-interval '10 minutes'
 FROM generate_series(1,520) n;
 UPDATE credential_limit_capacity SET used=520 WHERE kind='client'`)
	count, err := s.Collect(ctx)
	if err != nil || count != 500 {
		t.Fatalf("first GC=%d %v", count, err)
	}
	count, err = newLimits(t, pool).Collect(ctx)
	if err != nil || count != 0 {
		t.Fatalf("replica ignored shared GC schedule: %d %v", count, err)
	}
	execSQL(t, pool, "UPDATE credential_limit_settings SET last_gc_at=clock_timestamp()-interval '61 seconds'")
	count, err = s.Collect(ctx)
	if err != nil || count != 20 {
		t.Fatalf("second GC=%d %v", count, err)
	}
	if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "1" {
		t.Fatal("GC deleted live budget")
	}
	assertCapacity(t, pool)
}

func TestExistingDebitNeverNeedsCapacityLock(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	ctx := context.Background()
	if err := s.Account(ctx, "existing"); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT * FROM credential_limit_capacity WHERE kind='account' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	if err = s.Account(ctx, "existing"); err != nil {
		t.Fatalf("existing debit took capacity lock: %v", err)
	}
	deadline, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	failureIs(t, s.Account(deadline, "new-key"), 503)
	if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "1" {
		t.Fatal("blocked new key persisted")
	}
	assertCapacity(t, pool)
}

func TestCollectorSkipsDebitLocksAndSerializesReplicas(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	ctx := context.Background()
	if err := s.Account(ctx, "busy"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()-interval '40 minutes',expires_at=clock_timestamp()-interval '10 minutes'")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT * FROM credential_limit_buckets FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	count, err := s.Collect(ctx)
	if err != nil || count != 0 {
		t.Fatalf("locked bucket collected: %d %v", count, err)
	}
	if _, err = tx.Exec(ctx, "UPDATE credential_limit_buckets SET tat=clock_timestamp()+interval '12 seconds',expires_at=clock_timestamp()+interval '1812 seconds'"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, "UPDATE credential_limit_settings SET last_gc_at=clock_timestamp()-interval '61 seconds'")
	count, err = s.Collect(ctx)
	if err != nil || count != 0 {
		t.Fatalf("fresh debit collected: %d %v", count, err)
	}
	guard, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Rollback(ctx)
	if _, err = guard.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", gcLock); err != nil {
		t.Fatal(err)
	}
	count, err = s.Collect(ctx)
	if err != nil || count != 0 {
		t.Fatalf("GC loser waited or failed: %d %v", count, err)
	}
	assertCapacity(t, pool)
}

func TestConcurrentGCAndRecreateKeepExactCapacity(t *testing.T) {
	pool := limitDatabase(t, true)
	a, b := newLimits(t, pool), newLimits(t, pool)
	ctx := context.Background()
	if err := a.Account(ctx, "racing"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		execSQL(t, pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()-interval '40 minutes',expires_at=clock_timestamp()-interval '10 minutes'; UPDATE credential_limit_settings SET last_gc_at=clock_timestamp()-interval '61 seconds'")
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; _, err := a.Collect(ctx); results <- err }()
		go func() { <-start; results <- b.Account(ctx, "racing") }()
		close(start)
		for j := 0; j < 2; j++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "1" {
			t.Fatal("GC removed renewed budget")
		}
		assertCapacity(t, pool)
	}
}

func TestCollectorFailureRollsBackRowsCountAndSchedule(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	ctx := context.Background()
	if err := s.Account(ctx, "rollback"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()-interval '40 minutes',expires_at=clock_timestamp()-interval '10 minutes'")
	before := scalar(t, pool, "SELECT last_gc_at::text FROM credential_limit_settings")
	execSQL(t, pool, `CREATE FUNCTION fail_limit_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture GC rollback'; END $$;
 CREATE CONSTRAINT TRIGGER fail_limit_delete AFTER DELETE ON credential_limit_buckets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_limit_delete()`)
	_, err := s.Collect(ctx)
	failureIs(t, err, 503)
	if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "1" || scalar(t, pool, "SELECT last_gc_at::text FROM credential_limit_settings") != before {
		t.Fatal("failed GC changed rows/schedule")
	}
	assertCapacity(t, pool)
}

func TestFirstKeyBindingAndGCCannotInvertLockOrder(t *testing.T) {
	pool := limitDatabase(t, true)
	a, b := newLimits(t, pool), newLimits(t, pool)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		execSQL(t, pool, "TRUNCATE credential_limit_buckets; UPDATE credential_limit_capacity SET used=0; UPDATE credential_limit_settings SET key_fingerprint=NULL,last_gc_at='1970-01-01'")
		start := make(chan struct{})
		results := make(chan error, 3)
		go func() { <-start; results <- a.Account(ctx, "initial") }()
		go func() { <-start; results <- b.Account(ctx, "initial") }()
		go func() { <-start; _, err := b.Collect(ctx); results <- err }()
		close(start)
		for j := 0; j < 3; j++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "1" {
			t.Fatal("initialization lost budget")
		}
		assertCapacity(t, pool)
	}
}
