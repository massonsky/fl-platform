//go:build integration

package main

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

// Only a separate test PostgreSQL is allowed. Mount release SQL at /migrations;
// supply its test credentials via FL_SECRETS_DIR, never production secrets.
func bootstrapDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("FL_BOOTSTRAP_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_HOST") == "" ||
		os.Getenv("FL_PG_DATABASE") == "" || os.Getenv("FL_PG_DATABASE") == "flrp" {
		t.Fatal("requires FL_BOOTSTRAP_TEST_ISOLATED=1 and explicit test FL_PG_HOST/FL_PG_DATABASE (not flrp)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := "bootstrap_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	return pool
}

func bootstrapExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func bootstrapScalar(t *testing.T, pool *pgxpool.Pool, sql string) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(context.Background(), sql).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func releaseMigrations(t *testing.T) []migration {
	t.Helper()
	files, err := loadMigrations("/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 6 || files[5].name != "000006_ai_assistants" {
		t.Fatal("test requires the release migrations including AI assistants")
	}
	return files
}

func assertBootstrapLedger(t *testing.T, pool *pgxpool.Pool, expected []migration) {
	t.Helper()
	rows, err := pool.Query(context.Background(), "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range expected {
		names = append(names, file.name)
	}
	if !reflect.DeepEqual(actual, names) {
		t.Fatalf("migration ledger %v, expected %v", actual, names)
	}
}

func seedBootstrapBaseline(t *testing.T, pool *pgxpool.Pool) []migration {
	t.Helper()
	files := releaseMigrations(t)
	bootstrapExec(t, pool, "CREATE TABLE schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())")
	for _, file := range files[:3] {
		bootstrapExec(t, pool, file.sql)
		bootstrapExec(t, pool, "INSERT INTO schema_migrations(version) VALUES($1)", file.name)
	}
	return files
}

func TestBootstrapFreshAndRepeatedMigrations(t *testing.T) {
	pool := bootstrapDatabase(t)
	files := releaseMigrations(t)
	if err := migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	assertBootstrapLedger(t, pool, files)
	for _, table := range []string{"users", "model_presets", "administration_events", "ai_providers", "ai_conversations", "ai_generations", "ai_assistants", "organizations", "organization_memberships", "organization_events"} {
		var exists bool
		if err := pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists); err != nil || !exists {
			t.Fatalf("missing release table %s: %v", table, err)
		}
	}
	bootstrapExec(t, pool, "INSERT INTO users(external_subject,display_name,role) VALUES('preserved','Preserved user','researcher')")
	const snapshotSQL = "SELECT jsonb_agg(to_jsonb(s) ORDER BY version)::text FROM schema_migrations s"
	before := bootstrapScalar(t, pool, snapshotSQL)
	if err := migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if after := bootstrapScalar(t, pool, snapshotSQL); before != after {
		t.Fatal("repeat migration changed ledger/timestamps")
	}
	if bootstrapScalar(t, pool, "SELECT count(*)::text FROM users WHERE external_subject='preserved'") != "1" {
		t.Fatal("repeat migration changed existing user")
	}
}

func TestBootstrapUpgradeFromRecordedBaseline(t *testing.T) {
	pool := bootstrapDatabase(t)
	files := seedBootstrapBaseline(t, pool)
	bootstrapExec(t, pool, "INSERT INTO users(external_subject,display_name,role) VALUES('preserved','Preserved user','researcher')")
	if err := migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	assertBootstrapLedger(t, pool, files)
	if bootstrapScalar(t, pool, "SELECT count(*)::text FROM users WHERE external_subject='preserved'") != "1" {
		t.Fatal("upgrade changed existing user")
	}
}

func TestBootstrapRejectsUntrackedBaselineWithoutChanges(t *testing.T) {
	pool := bootstrapDatabase(t)
	bootstrapExec(t, pool, "CREATE TABLE users(id integer); INSERT INTO users VALUES(42)")
	if err := migrate(context.Background(), pool); err == nil || !strings.Contains(err.Error(), "untracked database schema") {
		t.Fatalf("expected untracked-schema refusal, got %v", err)
	}
	if bootstrapScalar(t, pool, "SELECT id::text FROM users") != "42" ||
		bootstrapScalar(t, pool, "SELECT (to_regclass('public.schema_migrations') IS NULL)::text") != "true" {
		t.Fatal("untracked schema was modified or ledger transaction did not roll back")
	}
}

func TestBootstrapRejectsUnknownLedgerWithoutChanges(t *testing.T) {
	pool := bootstrapDatabase(t)
	seedBootstrapBaseline(t, pool)
	bootstrapExec(t, pool, "INSERT INTO schema_migrations(version) VALUES('999999_unknown_release')")
	before := bootstrapScalar(t, pool, "SELECT jsonb_agg(to_jsonb(s) ORDER BY version)::text FROM schema_migrations s")
	if err := migrate(context.Background(), pool); err == nil || !strings.Contains(err.Error(), "migration ledger is not a prefix") {
		t.Fatalf("expected unknown-ledger refusal, got %v", err)
	}
	if bootstrapScalar(t, pool, "SELECT jsonb_agg(to_jsonb(s) ORDER BY version)::text FROM schema_migrations s") != before ||
		bootstrapScalar(t, pool, "SELECT (to_regclass('public.ai_providers') IS NULL)::text") != "true" {
		t.Fatal("unknown ledger was modified or migrations were applied")
	}
}

func TestBootstrapRollsBackAllPendingMigrationsOnConflict(t *testing.T) {
	pool := bootstrapDatabase(t)
	files := seedBootstrapBaseline(t, pool)
	// 000004 can execute, then 000005 conflicts: both its partial DDL and 000004 must roll back.
	bootstrapExec(t, pool, "CREATE TABLE ai_generations(preserved integer); INSERT INTO ai_generations VALUES(42)")
	if err := migrate(context.Background(), pool); err == nil || !strings.Contains(err.Error(), "000005_ai_chat") {
		t.Fatalf("expected conflicting-schema refusal, got %v", err)
	}
	assertBootstrapLedger(t, pool, files[:3])
	if bootstrapScalar(t, pool, "SELECT (to_regclass('public.ai_providers') IS NULL AND to_regclass('public.ai_conversations') IS NULL)::text") != "true" ||
		bootstrapScalar(t, pool, "SELECT preserved::text FROM ai_generations") != "42" {
		t.Fatal("failed migration changed untracked data or left partial DDL")
	}
}
