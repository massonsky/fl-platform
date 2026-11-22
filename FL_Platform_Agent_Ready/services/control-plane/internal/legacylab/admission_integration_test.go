//go:build integration

package legacylab

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func labDatabase(t *testing.T, version int) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("FL_LEGACY_LAB_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_HOST") == "" || os.Getenv("FL_PG_DATABASE") == "" || os.Getenv("FL_PG_DATABASE") == "flrp" || os.Getenv("FL_SECRETS_DIR") == "" {
		t.Fatal("requires FL_LEGACY_LAB_TEST_ISOLATED=1 and explicit isolated PG host/database/secrets, not flrp")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := "lab_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	files, err := filepath.Glob("/migrations/*.up.sql")
	if err != nil || len(files) < 9 || version < 6 || version > 9 {
		t.Fatal("requires release /migrations through9", err)
	}
	for _, file := range files[:version] {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		labExec(t, pool, string(raw))
	}
	return pool
}
func labExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func labScalar(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func labUser(t *testing.T, pool *pgxpool.Pool, role string) identity.Principal {
	t.Helper()
	p := identity.Principal{UserID: uuid.NewString(), SessionHash: identity.Hash(uuid.NewString())}
	labExec(t, pool, "INSERT INTO users(id,external_subject,display_name,role) VALUES($1::uuid,$1::text,$1::text,$2)", p.UserID, role)
	labExec(t, pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')", p.SessionHash, p.UserID)
	return p
}
func labFailure(t *testing.T, err error, status int, code string) {
	t.Helper()
	var f Failure
	if !errors.As(err, &f) || f.Status != status || f.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}
func requireLab(t *testing.T, pool *pgxpool.Pool, p identity.Principal) error {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = authorization.Read(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	u, err := identity.Current(context.Background(), tx, p)
	if err != nil {
		return safeFailure(err)
	}
	return Require(context.Background(), tx, u)
}
func labHash(t *testing.T, pool *pgxpool.Pool, includeAdmission bool) [32]byte {
	t.Helper()
	rows, err := pool.Query(context.Background(), "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename")
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	var data strings.Builder
	for _, table := range tables {
		if !includeAdmission && strings.HasPrefix(table, "legacy_lab_") {
			continue
		}
		data.WriteString(table + ":")
		data.WriteString(labScalar(t, pool, "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+pgx.Identifier{table}.Sanitize()+" t"))
	}
	return sha256.Sum256([]byte(data.String()))
}

func TestAdmissionBackfillPreservesAllLegacyRows(t *testing.T) {
	pool := labDatabase(t, 8)
	admin, researcher, disabled := labUser(t, pool, "admin"), labUser(t, pool, "researcher"), labUser(t, pool, "researcher")
	owner, viewer := labUser(t, pool, "viewer"), labUser(t, pool, "viewer")
	labExec(t, pool, "UPDATE users SET disabled_at=now() WHERE id=$1", disabled.UserID)
	project, experiment, provider, conversation := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	labExec(t, pool, "INSERT INTO projects(id,owner_id,slug,name) VALUES($1,$2,'legacy-owned','Private legacy')", project, owner.UserID)
	labExec(t, pool, `INSERT INTO experiments(id,project_id,name,config_json,config_hash) VALUES($1,$2,'Seeded','{"seed":11,"partition":"non-iid"}','unchanged-config')`, experiment, project)
	labExec(t, pool, `INSERT INTO runs(experiment_id,created_by,status,manifest_json,manifest_hash) VALUES($1,$2,'running','{"seed":11,"rounds":25}','unchanged-manifest')`, experiment, owner.UserID)
	labExec(t, pool, "INSERT INTO ai_providers(id,owner_id,name,base_url,encrypted_secret,key_version) VALUES($1,$2,'Private','https://fixture.invalid',decode('010203','hex'),'fixture')", provider, owner.UserID)
	labExec(t, pool, "INSERT INTO ai_conversations(id,owner_id,provider_id,model) VALUES($1,$2,$3,'private-fixture')", conversation, owner.UserID, provider)
	labExec(t, pool, "INSERT INTO ai_generations(owner_id,request_id,conversation_id,fingerprint,input,content,status,provider_revision) VALUES($1,$2,$3,'fixture','private input','private answer','completed',1)", owner.UserID, uuid.NewString(), conversation)
	before := labHash(t, pool, false)
	raw, err := os.ReadFile("/migrations/000009_legacy_lab_admission.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	labExec(t, pool, string(raw))
	if labHash(t, pool, false) != before {
		t.Fatal("migration changed legacy rows, private AI, seeds or sessions")
	}
	for _, p := range []identity.Principal{admin, researcher, disabled, owner} {
		if labScalar(t, pool, "SELECT status||':'||source||':'||revision::text FROM legacy_lab_admissions WHERE user_id=$1", p.UserID) != "active:legacy_backfill:1" {
			t.Fatal("eligible existing identity not backfilled")
		}
	}
	if labScalar(t, pool, "SELECT count(*)::text FROM legacy_lab_admissions WHERE user_id=$1", viewer.UserID) != "0" || labScalar(t, pool, "SELECT count(*)::text FROM legacy_lab_admission_events WHERE action='legacy_backfilled' AND actor_id IS NULL") != "4" {
		t.Fatal("backfill criterion or audit mismatch")
	}
	if err := requireLab(t, pool, researcher); err != nil {
		t.Fatal(err)
	}
	labFailure(t, requireLab(t, pool, owner), 403, "LEGACY_LAB_NOT_ADMITTED")
	labFailure(t, requireLab(t, pool, disabled), 401, "UNAUTHORIZED")
	for _, role := range []string{"admin", "researcher", "viewer"} {
		p := labUser(t, pool, role)
		a, err := (Service{pool}).Self(context.Background(), p)
		if err != nil || a.Status != "not_admitted" || a.Revision != 0 || a.CanCreateLegacyWork {
			t.Fatal("new identity inherited admission", err)
		}
		labFailure(t, requireLab(t, pool, p), 403, "LEGACY_LAB_NOT_ADMITTED")
	}
}

func TestAdmissionAbsentAndPartialSchemaFailsClosed(t *testing.T) {
	for _, version := range []int{6, 8, 9} {
		pool := labDatabase(t, version)
		p := labUser(t, pool, "admin")
		if version == 9 {
			labExec(t, pool, "ALTER TABLE legacy_lab_admission_events RENAME COLUMN reason TO unavailable_reason")
		}
		labFailure(t, requireLab(t, pool, p), 503, "DEPENDENCY_UNAVAILABLE")
		_, err := (Service{pool}).Self(context.Background(), p)
		labFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	}
	pool := labDatabase(t, 9)
	p := labUser(t, pool, "admin")
	if _, err := (Service{pool}).Change(context.Background(), p, p.UserID, Change{"active", 0, "Active fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := requireLab(t, pool, p); err != nil {
		t.Fatal(err)
	}
	// All expected names still exist, but an incompatible audit column must not
	// allow active admission through a read-only Require path.
	labExec(t, pool, `ALTER TABLE legacy_lab_admission_events ADD COLUMN incompatible_reason integer NOT NULL DEFAULT 1;
 ALTER TABLE legacy_lab_admission_events DROP COLUMN reason;
 ALTER TABLE legacy_lab_admission_events RENAME COLUMN incompatible_reason TO reason`)
	labFailure(t, requireLab(t, pool, p), 503, "DEPENDENCY_UNAVAILABLE")
	_, err := (Service{pool}).Self(context.Background(), p)
	labFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
}
