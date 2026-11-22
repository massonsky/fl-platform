//go:build integration

package tenantagent

import (
	"context"
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

func agentDatabase(t *testing.T, migrate bool) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("FL_TENANT_AGENT_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_DATABASE") != "release_check" || os.Getenv("FL_PG_HOST") == "" || os.Getenv("FL_SECRETS_DIR") == "" {
		t.Fatal("requires explicit tenant-agent isolation, host/secrets and release_check database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if scalar(t, admin, "SELECT current_database()") != "release_check" {
		admin.Close()
		t.Fatal("unexpected control database")
	}
	name := "tenant_agent_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if err != nil || len(files) < 11 || filepath.Base(files[10]) != "000011_tenant_agent_identity.up.sql" {
		t.Fatal("requires migrations1-11", err)
	}
	count := 10
	if migrate {
		count = 11
	}
	for _, path := range files[:count] {
		raw, err := os.ReadFile(path)
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
	var result string
	if err := pool.QueryRow(ctx, query, args...).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

type agentFixture struct{ user, otherUser, org, otherOrg, agent string }

func seedAgentScope(t *testing.T, pool *pgxpool.Pool, withAgent bool) agentFixture {
	t.Helper()
	f := agentFixture{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	for _, pair := range [][2]string{{f.user, f.org}, {f.otherUser, f.otherOrg}} {
		execSQL(t, pool, "INSERT INTO users(id,external_subject,display_name,role) VALUES($1,$2,'Fixture','researcher')", pair[0], "agent-test-"+pair[0])
		execSQL(t, pool, "INSERT INTO organizations(id,name,kind,created_by) VALUES($1,'Fixture','shared',$2)", pair[1], pair[0])
		execSQL(t, pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", pair[1], pair[0])
	}
	if withAgent {
		execSQL(t, pool, "INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by) VALUES($1,$2,1,'Fixture node',$3)", f.agent, f.org, f.user)
	}
	return f
}

// A populated old client/task/manifest/receipt chain makes compatibility checks
// meaningful. No runtime is started, and these IDs never escape the disposable DB.
func seedLegacy(t *testing.T, pool *pgxpool.Pool, f agentFixture) {
	t.Helper()
	p, e, r, ro, c := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	execSQL(t, pool, "INSERT INTO projects(id,owner_id,slug,name,organization_id,created_by) VALUES($1,$2,'unchanged-legacy','Legacy',$3,$2)", p, f.user, f.org)
	execSQL(t, pool, `INSERT INTO experiments(id,project_id,name,config_json,config_hash) VALUES($1,$2,'Legacy','{"clients":5,"seed":11}',repeat('a',64))`, e, p)
	execSQL(t, pool, `INSERT INTO runs(id,experiment_id,created_by,status,manifest_json,manifest_hash) VALUES($1,$2,$3,'queued','{"clients":5,"seed":11}',repeat('a',64))`, r, e, f.user)
	execSQL(t, pool, "INSERT INTO rounds(id,run_id,round_no,base_model_version,status) VALUES($1,$2,1,0,'planned')", ro, r)
	execSQL(t, pool, "INSERT INTO clients(id,display_name,identity_fingerprint,software_version,shard_index) VALUES($1,'Legacy','fixed-lab-fingerprint','0.1',0)", c)
	execSQL(t, pool, "INSERT INTO tasks(round_id,client_id,status) VALUES($1,$2,'available')", ro, c)
	execSQL(t, pool, `INSERT INTO http_receipts(user_id,operation,idempotency_key,fingerprint,response_status,response_body) VALUES($1,'queue','fixture-replay',repeat('b',64),202,'{"saved":true}')`, f.user)
}

func relationData(t *testing.T, pool *pgxpool.Pool, names []string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range names {
		relation := pgx.Identifier{"public", name}.Sanitize()
		result[name] = scalar(t, pool, "SELECT md5(coalesce(string_agg(to_jsonb(t)::text,E'\\n' ORDER BY to_jsonb(t)::text),'')) FROM "+relation+" t")
		result[name+"/schema"] = scalar(t, pool, `SELECT md5(coalesce(string_agg(v,E'\n' ORDER BY v),'')) FROM (
 SELECT 'c:'||a.attname||':'||a.atttypid::regtype::text||':'||a.attnotnull::text||':'||coalesce(pg_get_expr(d.adbin,d.adrelid),'') v
 FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL SELECT 'k:'||conname||':'||pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid=to_regclass($1)
 UNION ALL SELECT 'i:'||pg_get_indexdef(indexrelid) FROM pg_index WHERE indrelid=to_regclass($1)
 UNION ALL SELECT 't:'||pg_get_triggerdef(oid) FROM pg_trigger WHERE tgrelid=to_regclass($1) AND NOT tgisinternal
 ) definitions`, relation)
	}
	return result
}

func oldRelations(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	var names []string
	err := pool.QueryRow(context.Background(), "SELECT array_agg(relname::text ORDER BY relname) FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind='r'").Scan(&names)
	if err != nil || len(names) < 20 {
		t.Fatal("missing baseline relations", err)
	}
	return names
}
