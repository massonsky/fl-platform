//go:build integration

package tenantagent

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

// This covers public columns/constraints/indexes/triggers and this migration's
// function. Extension definitions and live backup rehearsal remain separate.
const catalogSignature = `SELECT md5(string_agg(v,E'\n' ORDER BY v)) FROM (
 SELECT 'c:'||c.relname||':'||a.attname||':'||a.atttypid::regtype::text||':'||a.attnotnull::text||':'||coalesce(pg_get_expr(d.adbin,d.adrelid),'') v
 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid
 LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
 WHERE c.relnamespace='public'::regnamespace AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL SELECT 'k:'||c.relname||':'||k.conname||':'||pg_get_constraintdef(k.oid)
 FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid WHERE c.relnamespace='public'::regnamespace
 UNION ALL SELECT 'i:'||indexdef FROM pg_indexes WHERE schemaname='public'
 UNION ALL SELECT 't:'||pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
 WHERE c.relnamespace='public'::regnamespace AND NOT t.tgisinternal
 UNION ALL SELECT 'f:'||pg_get_functiondef(p.oid) FROM pg_proc p
 WHERE p.pronamespace='public'::regnamespace AND p.proname='tenant_agent_immutable_owner'
 ) definitions`

func TestTenantAgentFreshUpgradePreservesLegacy(t *testing.T) {
	upgraded := agentDatabase(t, false)
	f := seedAgentScope(t, upgraded, false)
	seedLegacy(t, upgraded, f)
	names := oldRelations(t, upgraded)
	before := relationData(t, upgraded, names)
	assertFailure(t, schemaReady(context.Background(), upgraded), 503, "DEPENDENCY_UNAVAILABLE")
	raw, err := os.ReadFile("/migrations/000011_tenant_agent_identity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, upgraded, string(raw))
	if err = schemaReady(context.Background(), upgraded); err != nil {
		t.Fatal(err)
	}
	if after := relationData(t, upgraded, names); !reflect.DeepEqual(before, after) {
		t.Fatal("migration modified previous application data or relation definitions")
	}
	if scalar(t, upgraded, "SELECT count(*)::text FROM tenant_agents") != "0" {
		t.Fatal("migration implicitly adopted legacy clients")
	}
	fresh := agentDatabase(t, true)
	if scalar(t, fresh, catalogSignature) != scalar(t, upgraded, catalogSignature) {
		t.Fatal("fresh and populated upgrade schemas differ")
	}
	if err = schemaReady(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
}

func TestTenantAgentPartialSchemaFailsClosed(t *testing.T) {
	pool := agentDatabase(t, true)
	for _, change := range []string{
		"ALTER TABLE agent_events RENAME TO missing_events",
		"ALTER TABLE tenant_agents ALTER COLUMN state DROP NOT NULL",
		"ALTER TABLE tenant_agents ALTER COLUMN name TYPE varchar(120)",
		"ALTER TABLE agent_enrollments DROP CONSTRAINT agent_enrollments_pkey",
		"ALTER TABLE agent_credentials DROP CONSTRAINT agent_credentials_credential_id_key",
		"ALTER TABLE agent_credentials DROP CONSTRAINT agent_credentials_digest_key",
		"ALTER TABLE tenant_agents DROP CONSTRAINT tenant_agents_organization_id_registry_slot_key",
		"ALTER TABLE agent_credentials DROP CONSTRAINT agent_credentials_agent_id_organization_id_fkey",
		"ALTER TABLE agent_enrollments DROP CONSTRAINT agent_enrollments_organization_id_issuer_user_id_fkey",
		"ALTER TABLE agent_enrollments DROP CONSTRAINT agent_enrollments_times",
		"ALTER TABLE tenant_agents DISABLE TRIGGER tenant_agent_immutable_owner",
		"DROP TRIGGER tenant_agent_immutable_owner ON tenant_agents; CREATE TRIGGER tenant_agent_immutable_owner BEFORE UPDATE ON tenant_agents FOR EACH ROW WHEN (false) EXECUTE FUNCTION tenant_agent_immutable_owner()",
		"ALTER TABLE agent_credentials DROP CONSTRAINT agent_credentials_agent_id_organization_id_fkey; ALTER TABLE agent_credentials ADD FOREIGN KEY(agent_id,organization_id) REFERENCES tenant_agents(id,organization_id) ON DELETE CASCADE",
	} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(ctx, change); err != nil {
				t.Fatal(err)
			}
			assertFailure(t, schemaReady(ctx, tx), 503, "DEPENDENCY_UNAVAILABLE")
		})
	}
	if err := schemaReady(context.Background(), pool); err != nil {
		t.Fatal("partial schema tests did not roll back", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertFailure(t, schemaReady(ctx, pool), 503, "DEPENDENCY_UNAVAILABLE")
	assertFailure(t, schemaReady(context.Background(), nil), 503, "DEPENDENCY_UNAVAILABLE")
	if scalar(t, pool, "SELECT count(*)::text FROM agent_events") != "0" {
		t.Fatal("readiness probe wrote state")
	}
}
