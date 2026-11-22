//go:build integration

package projectaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func accessDatabase(t *testing.T, beforeAccess func(*pgxpool.Pool)) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("FL_PROJECT_ACCESS_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_HOST") == "" || os.Getenv("FL_PG_DATABASE") == "" || os.Getenv("FL_PG_DATABASE") == "flrp" || os.Getenv("FL_SECRETS_DIR") == "" {
		t.Fatal("requires isolated test PG host/database/secrets and FL_PROJECT_ACCESS_TEST_ISOLATED=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := "project_access_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if err != nil || len(files) < 8 {
		t.Fatal("requires /migrations through 000008", err)
	}
	for _, file := range files {
		if strings.HasPrefix(filepath.Base(file), "000008_") && beforeAccess != nil {
			beforeAccess(pool)
		}
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		accessExec(t, pool, string(sql))
	}
	return pool
}
func accessExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func accessScalar(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func accessUser(t *testing.T, pool *pgxpool.Pool, role string) string {
	t.Helper()
	id := uuid.NewString()
	accessExec(t, pool, "INSERT INTO users(id,external_subject,display_name,role) VALUES($1::uuid,$1::text,$1::text,$2)", id, role)
	return id
}
func accessOrg(t *testing.T, pool *pgxpool.Pool, owner string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := (organization.Store{Pool: pool}).Create(context.Background(), owner, id, "Access fixture"); err != nil {
		t.Fatal(err)
	}
	return id
}
func accessMember(t *testing.T, pool *pgxpool.Pool, org, globalRole, role string) string {
	t.Helper()
	id := accessUser(t, pool, globalRole)
	accessExec(t, pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,$3)", org, id, role)
	return id
}
func accessFailure(t *testing.T, err error, status int, code string) {
	t.Helper()
	var f Failure
	if !errors.As(err, &f) || f.Status != status || f.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}
func accessPGFailure(t *testing.T, err error, code string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != code {
		t.Fatalf("got %v, want SQLSTATE %s", err, code)
	}
}
func accessFixture(t *testing.T) (*pgxpool.Pool, Store, string, string, string) {
	t.Helper()
	pool := accessDatabase(t, nil)
	s := Store{Pool: pool}
	owner := accessUser(t, pool, "researcher")
	org := accessOrg(t, pool, owner)
	id := uuid.NewString()
	if _, err := s.Create(context.Background(), owner, org, id, CreateInput{"fixture", "Fixture"}); err != nil {
		t.Fatal(err)
	}
	return pool, s, owner, org, id
}

func TestAccessMigrationPreservesLegacyAndPrivateAI(t *testing.T) {
	var owner, org, legacy, provider, before string
	pool := accessDatabase(t, func(pool *pgxpool.Pool) {
		owner = accessUser(t, pool, "researcher")
		org, legacy, provider = uuid.NewString(), uuid.NewString(), uuid.NewString()
		accessExec(t, pool, "INSERT INTO organizations(id,name,kind,created_by,personal_owner_id,state) VALUES($1,'Personal','personal',$2,$2,'suspended')", org, owner)
		accessExec(t, pool, "INSERT INTO organization_memberships(organization_id,user_id,role,status) VALUES($1,$2,'owner','revoked')", org, owner)
		accessExec(t, pool, "INSERT INTO projects(id,owner_id,organization_id,slug,name) VALUES($1,$2,$3,'legacy','Legacy')", legacy, owner, org)
		accessExec(t, pool, "INSERT INTO ai_providers(id,owner_id,name,base_url,encrypted_secret,key_version) VALUES($1,$2,'Private','https://fixture.invalid/v1','private-ciphertext','fixture')", provider, owner)
		before = accessScalar(t, pool, "SELECT to_jsonb(p)::text FROM ai_providers p WHERE id=$1", provider)
	})
	if after := accessScalar(t, pool, "SELECT to_jsonb(p)::text FROM ai_providers p WHERE id=$1", provider); after != before {
		t.Fatal("private AI modified")
	}
	s := Store{Pool: pool}
	p, err := s.Get(context.Background(), owner, ResourceRef{Kind: "project", ID: legacy})
	if err != nil || p.CreatedBy != owner || !p.Capabilities.StartRun || p.AccessSource != "legacy_owner" {
		t.Fatal(p, err)
	}
	foreign := accessUser(t, pool, "admin")
	_, err = s.Get(context.Background(), foreign, ResourceRef{Kind: "project", ID: legacy})
	accessFailure(t, err, 404, "NOT_FOUND")
	accessExec(t, pool, "INSERT INTO projects(owner_id,slug,name) VALUES($1,'post-migration','Old client')", owner)
	if accessScalar(t, pool, "SELECT (created_by IS NULL AND access_mode='legacy_private' AND visibility='private')::text FROM projects WHERE slug='post-migration'") != "true" {
		t.Fatal("legacy INSERT compatibility lost")
	}
	_, err = pool.Exec(context.Background(), "INSERT INTO projects(owner_id,slug,name) VALUES($1,'legacy','Duplicate')", owner)
	accessPGFailure(t, err, "23505")
	var pg *pgconn.PgError
	errors.As(err, &pg)
	if pg.ConstraintName != "projects_slug_key" {
		t.Fatal("legacy error mapper compatibility lost")
	}
	_, err = pool.Exec(context.Background(), "UPDATE projects SET visibility='organization' WHERE id=$1", legacy)
	accessPGFailure(t, err, "23514")
	accessExec(t, pool, "UPDATE users SET role='viewer' WHERE id=$1", owner)
	p, err = s.Get(context.Background(), owner, ResourceRef{Kind: "project", ID: legacy})
	if err != nil || !p.Capabilities.Read || p.Capabilities.CreateExperiment {
		t.Fatal("legacy viewer cap", p, err)
	}
}

func TestAccessCreateReplaySlugIsolationAndCompositeFK(t *testing.T) {
	pool, s, owner, org, id := accessFixture(t)
	ctx := context.Background()
	p, err := s.Create(ctx, owner, org, id, CreateInput{"fixture", " Fixture "})
	if err != nil || p.Visibility != "private" || !p.Capabilities.ManageAccess {
		t.Fatal(p, err)
	}
	_, err = s.Create(ctx, owner, org, id, CreateInput{"fixture", "Different"})
	accessFailure(t, err, 409, "VERSION_CONFLICT")
	_, err = s.Create(ctx, owner, org, uuid.NewString(), CreateInput{"fixture", "Duplicate"})
	accessFailure(t, err, 409, "SLUG_CONFLICT")
	other := accessUser(t, pool, "admin")
	otherOrg := accessOrg(t, pool, other)
	if _, err = s.Create(ctx, other, otherOrg, uuid.NewString(), CreateInput{"fixture", "Other tenant"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(ctx, other, otherOrg, id, CreateInput{"fixture", "Fixture"})
	accessFailure(t, err, 404, "NOT_FOUND")
	accessExec(t, pool, "INSERT INTO projects(owner_id,slug,name) VALUES($1,'fixture','Legacy slug')", owner)
	_, err = pool.Exec(ctx, "INSERT INTO project_members(project_id,organization_id,user_id,role,status,granted_by) VALUES($1,$2,$3,'viewer','active',$4)", id, otherOrg, other, owner)
	accessPGFailure(t, err, "23503")
	_, err = pool.Exec(ctx, "INSERT INTO project_members(project_id,organization_id,user_id,role,status,granted_by) VALUES($1,$2,$3,'viewer','active',$4)", id, org, other, owner)
	accessPGFailure(t, err, "23503")
	_, err = pool.Exec(ctx, "INSERT INTO projects(owner_id,slug,name,access_mode) VALUES($1,'invalid-org','Invalid','organization')", owner)
	accessPGFailure(t, err, "23514")
	researcher := accessMember(t, pool, org, "researcher", "researcher")
	_, err = s.Create(ctx, researcher, org, uuid.NewString(), CreateInput{"denied", "Denied"})
	accessFailure(t, err, 403, "FORBIDDEN")
	if accessScalar(t, pool, "SELECT count(*)::text FROM project_access_events WHERE project_id=$1", id) != "1" {
		t.Fatal("failed/replayed create changed audit")
	}
}
