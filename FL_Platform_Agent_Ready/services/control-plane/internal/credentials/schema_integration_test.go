//go:build integration

package credentials

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func limitDatabase(t *testing.T, migrate bool) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("FL_CREDENTIAL_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_DATABASE") != "release_check" || os.Getenv("FL_PG_HOST") == "" || os.Getenv("FL_SECRETS_DIR") == "" {
		t.Fatal("requires isolated marker, explicit host/secrets and release_check database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if scalar(t, admin, "SELECT current_database()") != "release_check" {
		admin.Close()
		t.Fatal("wrong fixture database")
	}
	name := "credential_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	cfg := admin.Config()
	cfg.ConnConfig.Database = name
	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("/migrations/*.up.sql")
	if err != nil || len(files) < 10 || filepath.Base(files[9]) != "000010_credential_limits.up.sql" {
		t.Fatal("requires release migrations1-10", err)
	}
	count := 9
	if migrate {
		count = 10
	}
	for _, file := range files[:count] {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, pool, string(raw))
	}
	return pool
}

func execSQL(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func scalar(t *testing.T, pool *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var value string
	if err := pool.QueryRow(ctx, query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func newLimits(t *testing.T, pool *pgxpool.Pool) *Store {
	t.Helper()
	s, err := New(pool, bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func failureIs(t *testing.T, err error, status int) Failure {
	t.Helper()
	var failure Failure
	if !errors.As(err, &failure) || failure.Status != status {
		t.Fatalf("got %v, want safe%d", err, status)
	}
	if status == 503 && (failure.Code != "DEPENDENCY_UNAVAILABLE" || failure.RetryAfter != 3) {
		t.Fatalf("unsafe dependency failure: %+v", failure)
	}
	return failure
}

func assertCapacity(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if scalar(t, pool, `SELECT count(*)::text FROM credential_limit_capacity c
 WHERE used<>(SELECT count(*) FROM credential_limit_buckets b WHERE b.kind=c.kind) OR used<0 OR used>16384`) != "0" {
		t.Fatal("capacity invariant broken")
	}
}

func TestIncompleteCredentialSchemaFailsClosed(t *testing.T) {
	for _, change := range []string{
		"absent",
		"DROP TABLE credential_limit_settings",
		"ALTER TABLE credential_limit_settings ALTER COLUMN policy_version DROP NOT NULL",
		"ALTER TABLE credential_limit_settings ALTER COLUMN last_gc_at TYPE text USING last_gc_at::text",
		"ALTER TABLE credential_limit_buckets DROP CONSTRAINT credential_limit_buckets_pkey",
		"ALTER TABLE credential_limit_settings DROP CONSTRAINT credential_limit_settings_pkey",
		"DELETE FROM credential_limit_capacity WHERE kind='actor'",
		"UPDATE credential_limit_settings SET policy_version='different'",
	} {
		t.Run(change, func(t *testing.T) {
			pool := limitDatabase(t, change != "absent")
			if change != "absent" {
				execSQL(t, pool, change)
			}
			s := newLimits(t, pool)
			failureIs(t, s.Account(context.Background(), "not-in-database"), 503)
			_, err := s.Collect(context.Background())
			failureIs(t, err, 503)
			if change != "absent" && scalar(t, pool, "SELECT count(*)::text FROM credential_limit_buckets") != "0" {
				t.Fatal("partial schema allowed mutation")
			}
		})
	}
}

func TestCredentialKeysPolicyAndSafeErrors(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	if err := s.Account(context.Background(), "private-account-name"); err != nil {
		t.Fatal(err)
	}
	other, err := New(pool, bytes.Repeat([]byte{38}, 32))
	if err != nil {
		t.Fatal(err)
	}
	before := scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets WHERE kind='account'")
	failureIs(t, other.Account(context.Background(), "private-account-name"), 503)
	if scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets WHERE kind='account'") != before {
		t.Fatal("wrong key changed budget")
	}
	if err := newLimits(t, pool).Account(context.Background(), "private-account-name"); err != nil {
		t.Fatal(err)
	}
	raw := scalar(t, pool, "SELECT jsonb_agg(to_jsonb(b))::text FROM credential_limit_buckets b")
	if strings.Contains(raw, "private-account-name") || strings.Contains(raw, string(s.key[:])) {
		t.Fatal("raw key/identifier stored")
	}
	if scalar(t, pool, "SELECT bool_and(octet_length(digest)=32)::text FROM credential_limit_buckets") != "true" {
		t.Fatal("invalid digest")
	}
	_, err = New(pool, []byte("short"))
	failureIs(t, err, 503)
	var absent *Store
	failureIs(t, absent.Account(context.Background(), "someone"), 503)
	assertCapacity(t, pool)
}

func TestCredentialSQLFailureAndExpiredContextDoNotDebit(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	if err := s.Account(context.Background(), "locked"); err != nil {
		t.Fatal(err)
	}
	before := scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets")
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), "SELECT * FROM credential_limit_buckets FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	failureIs(t, s.Account(ctx, "locked"), 503)
	if time.Since(start) > time.Second {
		t.Fatal("caller deadline not bounded")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	failureIs(t, s.Account(ctx2, "different"), 503)
	if scalar(t, pool, "SELECT tat::text FROM credential_limit_buckets") != before {
		t.Fatal("failed SQL consumed account credit")
	}
	assertCapacity(t, pool)
}

func TestMissingKeyBindingCannotResetExistingBudgets(t *testing.T) {
	pool := limitDatabase(t, true)
	s := newLimits(t, pool)
	if err := s.Account(context.Background(), "existing"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, "UPDATE credential_limit_settings SET key_fingerprint=NULL")
	failureIs(t, s.Account(context.Background(), "another"), 503)
	_, err := s.Collect(context.Background())
	failureIs(t, err, 503)
	if scalar(t, pool, "SELECT (key_fingerprint IS NULL)::text FROM credential_limit_settings") != "true" {
		t.Fatal("partial metadata rebound key")
	}
	assertCapacity(t, pool)
	// A lost bucket with retained capacity is also inconsistent, not a clean install.
	execSQL(t, pool, "TRUNCATE credential_limit_buckets")
	failureIs(t, s.Account(context.Background(), "another"), 503)
}
