//go:build integration

package organization

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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func organizationDatabase(t *testing.T, beforeOrganizations func(*pgxpool.Pool)) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("FL_ORGANIZATION_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_HOST") == "" ||
		os.Getenv("FL_PG_DATABASE") == "" || os.Getenv("FL_PG_DATABASE") == "flrp" || os.Getenv("FL_SECRETS_DIR") == "" {
		t.Fatal("requires FL_ORGANIZATION_TEST_ISOLATED=1, explicit test PG host/database (not flrp), test FL_SECRETS_DIR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := "organization_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if err != nil || len(files) < 7 {
		t.Fatalf("requires release migrations at /migrations: %v", err)
	}
	for _, file := range files {
		if strings.HasPrefix(filepath.Base(file), "000007_") && beforeOrganizations != nil {
			beforeOrganizations(pool)
		}
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		organizationExec(t, pool, string(sql))
	}
	return pool
}

func organizationExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func organizationScalar(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func organizationUser(t *testing.T, pool *pgxpool.Pool, role string) string {
	t.Helper()
	id := uuid.NewString()
	organizationExec(t, pool, "INSERT INTO users(id,external_subject,display_name,role) VALUES($1::uuid,$1::text,$1::text,$2)", id, role)
	return id
}

func requireOrganizationFailure(t *testing.T, err error, status int, code string) {
	t.Helper()
	var failure Failure
	if !errors.As(err, &failure) || failure.Status != status || failure.Code != code {
		t.Fatalf("error %v, want %d %s", err, status, code)
	}
}

func TestOrganizationCreateReplayIsolationAndQuota(t *testing.T) {
	pool := organizationDatabase(t, nil)
	s := Store{Pool: pool}
	ctx := context.Background()
	actor := organizationUser(t, pool, "researcher")
	other := organizationUser(t, pool, "admin")
	id := uuid.NewString()
	o, err := s.Create(ctx, actor, id, "  Организация  ")
	if err != nil || o.Name != "Организация" || o.Role != "owner" || !o.CanManageMembers {
		t.Fatalf("create %+v: %v", o, err)
	}
	replay, err := s.Create(ctx, actor, id, "Организация")
	if err != nil || replay != o {
		t.Fatalf("replay %+v: %v", replay, err)
	}
	_, err = s.Create(ctx, actor, id, "Другое имя")
	requireOrganizationFailure(t, err, 409, "VERSION_CONFLICT")
	_, err = s.Create(ctx, other, id, "Организация")
	requireOrganizationFailure(t, err, 404, "NOT_FOUND")
	items, err := s.List(ctx, other)
	if err != nil || len(items) != 0 {
		t.Fatalf("platform administrator crossed scope: %+v %v", items, err)
	}
	for _, name := range []string{"", "  ", "bad\x00name", "bad\nname", "\xff", strings.Repeat("я", 121)} {
		_, err = s.Create(ctx, actor, uuid.NewString(), name)
		requireOrganizationFailure(t, err, 400, "INVALID_REQUEST")
	}
	if _, err = s.Create(ctx, actor, uuid.NewString(), strings.Repeat("я", 120)); err != nil {
		t.Fatal(err)
	}
	for i := 2; i < MaxCreatedSharedOrganizations; i++ {
		if _, err = s.Create(ctx, actor, uuid.NewString(), "Quota fixture"); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Create(ctx, actor, uuid.NewString(), "Over limit")
	requireOrganizationFailure(t, err, 409, "ORGANIZATION_LIMIT")
	if _, err = s.Create(ctx, actor, id, "Организация"); err != nil {
		t.Fatalf("replay at quota: %v", err)
	}
	if got := organizationScalar(t, pool, "SELECT count(*)::text FROM organization_events WHERE organization_id=$1", id); got != "1" {
		t.Fatalf("replay emitted duplicate audit: %s", got)
	}
}

func TestOrganizationLegacyBackfillPreservesCapsAndPrivateAI(t *testing.T) {
	var owners []string
	var privateBefore string
	const privateSQL = `SELECT jsonb_build_object('providers',(SELECT jsonb_agg(to_jsonb(p)) FROM ai_providers p),
 'chats',(SELECT jsonb_agg(to_jsonb(c)) FROM ai_conversations c),'assistants',(SELECT jsonb_agg(to_jsonb(a)) FROM ai_assistants a))::text`
	pool := organizationDatabase(t, func(pool *pgxpool.Pool) {
		for _, role := range []string{"researcher", "viewer", "admin"} {
			id := organizationUser(t, pool, role)
			owners = append(owners, id)
			organizationExec(t, pool, "INSERT INTO projects(owner_id,slug,name) VALUES($1::uuid,$1::text,'Legacy')", id)
		}
		organizationExec(t, pool, "INSERT INTO projects(owner_id,slug,name) VALUES($1,$2,'Second legacy project')", owners[0], uuid.NewString())
		organizationExec(t, pool, "UPDATE users SET disabled_at=now() WHERE id=$1", owners[2])
		provider := uuid.NewString()
		organizationExec(t, pool, `INSERT INTO ai_providers(id,owner_id,name,base_url,encrypted_secret,key_version)
 VALUES($1,$2,'Private','https://fixture.invalid/v1','fixture-ciphertext','fixture')`, provider, owners[0])
		organizationExec(t, pool, "INSERT INTO ai_conversations(owner_id,provider_id,model) VALUES($1,$2,'fixture')", owners[0], provider)
		organizationExec(t, pool, "INSERT INTO ai_assistants(id,owner_id,provider_id,name,model,instruction) VALUES($1,$2,$3,'Private','fixture','Private fixture')", uuid.NewString(), owners[0], provider)
		privateBefore = organizationScalar(t, pool, privateSQL)
	})
	if organizationScalar(t, pool, privateSQL) != privateBefore {
		t.Fatal("personal AI records changed during tenant backfill")
	}
	if organizationScalar(t, pool, `SELECT count(*)::text FROM projects p JOIN organizations o ON o.id=p.organization_id
	 WHERE o.personal_owner_id=p.owner_id AND o.created_by=p.owner_id AND o.kind='personal'`) != "4" ||
		organizationScalar(t, pool, "SELECT count(*)::text FROM organizations WHERE kind='personal'") != "3" {
		t.Fatal("legacy owner backfill mismatch")
	}
	s := Store{Pool: pool}
	for i, owner := range owners {
		org := organizationScalar(t, pool, "SELECT organization_id::text FROM projects WHERE owner_id=$1", owner)
		if i > 0 {
			_, err := s.ChangeMember(context.Background(), owner, org, owner, MemberChange{"admin", "active", 1})
			requireOrganizationFailure(t, err, 403, "FORBIDDEN")
			_, err = s.Create(context.Background(), owner, uuid.NewString(), "Denied")
			requireOrganizationFailure(t, err, 403, "FORBIDDEN")
		}
		items, err := s.List(context.Background(), owner)
		if err != nil || (i == 2 && len(items) != 0) || (i < 2 && len(items) != 1) || (i == 1 && items[0].CanManageMembers) {
			t.Fatalf("legacy cap mismatch for %d: %+v %v", i, items, err)
		}
	}
}
