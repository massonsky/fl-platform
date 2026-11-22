//go:build integration

package credentials

import (
	"bytes"
	"context"
	"net/netip"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestSharedGCRABurstRefillAndDeniedState(t *testing.T) {
	pool := limitDatabase(t, true)
	a, b := newLimits(t, pool), newLimits(t, pool)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		s := a
		if i%2 == 1 {
			s = b
		}
		if err := s.Account(ctx, "Known"); err != nil {
			t.Fatal(err)
		}
	}
	before := scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets WHERE kind='account'")
	denied := failureIs(t, newLimits(t, pool).Account(ctx, "Known"), 429)
	if denied.Code != "RATE_LIMITED" || denied.RetryAfter < 11 || denied.RetryAfter > 12 {
		t.Fatalf("bad retry: %+v", denied)
	}
	if scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets WHERE kind='account'") != before {
		t.Fatal("denied attempt extended cooldown")
	}
	for _, name := range []string{"unknown", "known"} {
		if err := b.Account(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	// Refill is evaluated against PostgreSQL time, not a process wall clock.
	execSQL(t, pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()-interval '1 second',expires_at=clock_timestamp()+interval '30 minutes' WHERE digest=$1", a.digest("account", "Known"))
	if err := a.Account(ctx, "Known"); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, pool)
}

func TestParallelSameKeyNeverOverdrawsOrDoubleAllocates(t *testing.T) {
	pool := limitDatabase(t, true)
	a, b := newLimits(t, pool), newLimits(t, pool)
	start := make(chan struct{})
	results := make(chan error, 24)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s := a
			if i%2 == 1 {
				s = b
			}
			results <- s.Account(context.Background(), "parallel")
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	allowed := 0
	for err := range results {
		if err == nil {
			allowed++
		} else {
			failureIs(t, err, 429)
		}
	}
	if allowed != 5 {
		t.Fatalf("admitted %d of a five-unit burst", allowed)
	}
	if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "1" {
		t.Fatal("same key allocated more than once")
	}
	assertCapacity(t, pool)
}

func TestUnitsClientFirstAndCanonicalKeys(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	ctx := context.Background()
	actor := uuid.NewString()
	if err := s.Actor(ctx, actor, 2); err != nil {
		t.Fatal(err)
	}
	failureIs(t, s.Actor(ctx, actor, 2), 429)
	if err := s.Actor(ctx, actor, 1); err != nil {
		t.Fatal(err)
	}
	failureIs(t, s.Actor(ctx, actor, 1), 429)
	client := netip.MustParseAddr("192.0.2.4")
	for i := 0; i < 10; i++ {
		if err := s.Client(ctx, client, 1); err != nil {
			t.Fatal(err)
		}
	}
	failureIs(t, s.Client(ctx, netip.MustParseAddr("::ffff:192.0.2.4"), 1), 429)
	if err := s.Client(ctx, netip.MustParseAddr("192.0.2.5"), 1); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"2001:db8:1:2::1", "2001:db8:1:2::f"} {
		if err := s.Client(ctx, netip.MustParseAddr(ip), 1); err != nil {
			t.Fatal(err)
		}
	}
	if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets WHERE kind='client'") != "3" {
		t.Fatal("client canonicalization inconsistent")
	}
	for i := 0; i < 5; i++ {
		if err := s.Account(ctx, "target"); err != nil {
			t.Fatal(err)
		}
	}
	other := netip.MustParseAddr("198.51.100.7")
	if err := s.Client(ctx, other, 1); err != nil {
		t.Fatal(err)
	}
	before := scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets WHERE kind='client' AND digest=$1", s.digest("client", "198.51.100.7/32"))
	failureIs(t, s.Account(ctx, "target"), 429)
	if scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets WHERE kind='client' AND digest=$1", s.digest("client", "198.51.100.7/32")) != before {
		t.Fatal("account denial refunded client budget")
	}
	assertCapacity(t, pool)
}

func TestInvalidCredentialKeysAreRejectedBeforeState(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	ctx := context.Background()
	for _, name := range []string{"", string(bytes.Repeat([]byte{'x'}, 65)), "bad\x00name", string([]byte{255})} {
		failureIs(t, s.Account(ctx, name), 400)
	}
	failureIs(t, s.Client(ctx, netip.Addr{}, 1), 400)
	failureIs(t, s.Client(ctx, netip.MustParseAddr("fe80::1%eth0"), 1), 400)
	failureIs(t, s.Actor(ctx, "not-an-id", 1), 400)
	failureIs(t, s.Actor(ctx, uuid.Nil.String(), 1), 400)
	for _, units := range []int{-1, 0, 3} {
		failureIs(t, s.Client(ctx, netip.MustParseAddr("192.0.2.1"), units), 400)
	}
	if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "0" {
		t.Fatal("invalid input stored")
	}
}

func TestMixedKeyFirstUseAndAllocationRollback(t *testing.T) {
	pool := limitDatabase(t, true)
	a := newLimits(t, pool)
	b, err := New(pool, bytes.Repeat([]byte{99}, 32))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		go func(s *Store) { <-start; results <- s.Account(context.Background(), "first") }(s)
	}
	close(start)
	allowed := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			allowed++
		} else {
			failureIs(t, err, 503)
		}
	}
	if allowed != 1 {
		t.Fatalf("mixed keys admitted %d", allowed)
	}
	assertCapacity(t, pool)
	execSQL(t, pool, "TRUNCATE credential_limit_buckets; UPDATE credential_limit_capacity SET used=0; UPDATE credential_limit_settings SET key_fingerprint=NULL")
	execSQL(t, pool, `CREATE FUNCTION fail_limit_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rollback'; END $$;
 CREATE CONSTRAINT TRIGGER fail_limit_insert AFTER INSERT ON credential_limit_buckets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_limit_insert()`)
	failureIs(t, a.Account(context.Background(), "rollback"), 503)
	if scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "0" || scalar(t, pool, "SELECT (key_fingerprint IS NULL)::text FROM credential_limit_settings") != "true" {
		t.Fatal("failed commit left key/bucket state")
	}
	assertCapacity(t, pool)
}

func TestFarFutureBudgetCannotOverflowRetryOrResetCredit(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	ctx := context.Background()
	if err := s.Account(ctx, "clock-shift"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()+interval '400 years',expires_at=clock_timestamp()+interval '400 years 30 minutes'")
	before := scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets")
	denied := failureIs(t, s.Account(ctx, "clock-shift"), 429)
	if denied.RetryAfter <= 0 {
		t.Fatalf("invalid retry after clock shift: %d", denied.RetryAfter)
	}
	if scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets") != before {
		t.Fatal("clock shift reset credit")
	}
	assertCapacity(t, pool)
}
