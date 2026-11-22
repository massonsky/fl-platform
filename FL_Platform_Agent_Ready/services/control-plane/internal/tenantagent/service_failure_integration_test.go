//go:build integration

package tenantagent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAgentServiceSchemaAndDatabaseFailures(t *testing.T) {
	t.Run("schema10", func(t *testing.T) {
		pool := agentDatabase(t, false)
		f := seedAgentScope(t, pool, false)
		s := serviceFixture{pool: pool, svc: Service{Pool: pool}, f: f, p: agentSession(t, pool, f.user)}
		denyService(t, s, s.p, f.org, 503, "DEPENDENCY_UNAVAILABLE")
	})
	for _, ddl := range []string{
		"DROP TABLE agent_issue_budgets",
		"ALTER TABLE agent_credentials RENAME COLUMN revoked_at TO unavailable_column",
		"ALTER TABLE tenant_agents DROP CONSTRAINT tenant_agents_slot",
		"ALTER TABLE sessions RENAME TO unavailable_sessions",
	} {
		t.Run(ddl, func(t *testing.T) {
			f := agentServiceFixture(t, true)
			execSQL(t, f.pool, ddl)
			denyService(t, f, f.p, f.f.org, 503, "DEPENDENCY_UNAVAILABLE")
		})
	}
	t.Run("nil and closed pool", func(t *testing.T) {
		f := agentServiceFixture(t, true)
		f.svc = Service{}
		denyService(t, f, f.p, f.f.org, 503, "DEPENDENCY_UNAVAILABLE")
		f.pool.Close()
		f.svc = Service{Pool: f.pool}
		denyService(t, f, f.p, f.f.org, 503, "DEPENDENCY_UNAVAILABLE")
	})
}

func TestAgentServiceAuditAndCommitRollback(t *testing.T) {
	f := agentServiceFixture(t, false)
	execSQL(t, f.pool, `CREATE FUNCTION reject_agent_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'fixture audit rejection'; END $$`)
	for _, trigger := range []string{
		"CREATE TRIGGER reject_agent_audit BEFORE INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION reject_agent_audit()",
		"CREATE CONSTRAINT TRIGGER reject_agent_audit AFTER INSERT ON agent_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_agent_audit()",
	} {
		execSQL(t, f.pool, trigger)
		before := agentState(t, f.pool)
		a, err := f.svc.Create(context.Background(), f.p, f.f.org, f.f.agent, "Rollback")
		serviceError(t, err, 503, "DEPENDENCY_UNAVAILABLE")
		if a != (Agent{}) {
			t.Fatal("failed audit/commit returned agent")
		}
		unchangedAgentState(t, f.pool, before)
		execSQL(t, f.pool, "DROP TRIGGER reject_agent_audit ON agent_events")
	}
	a, err := f.svc.Create(context.Background(), f.p, f.f.org, f.f.agent, "Rollback")
	if err != nil || a.Revision != 1 || scalar(t, f.pool, "SELECT registry_slot::text FROM tenant_agents WHERE id=$1", a.ID) != "1" {
		t.Fatal("rollback consumed identity or slot", a, err)
	}
}

func TestAgentServiceSQLDeadlineAndGuardRelease(t *testing.T) {
	f := agentServiceFixture(t, false)
	execSQL(t, f.pool, `CREATE FUNCTION delay_agent_insert() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_sleep(15); RETURN NEW; END $$;
 CREATE TRIGGER delay_agent_insert BEFORE INSERT ON tenant_agents FOR EACH ROW EXECUTE FUNCTION delay_agent_insert()`)
	before := agentState(t, f.pool)
	started := time.Now()
	a, err := f.svc.Create(context.Background(), f.p, f.f.org, f.f.agent, "Delayed")
	elapsed := time.Since(started)
	serviceError(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if a != (Agent{}) || elapsed < 9*time.Second || elapsed > 12*time.Second {
		t.Fatal("bounded SQL callback deadline", a, elapsed)
	}
	unchangedAgentState(t, f.pool, before)
	execSQL(t, f.pool, "DROP TRIGGER delay_agent_insert ON tenant_agents")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = f.svc.Create(ctx, f.p, f.f.org, f.f.agent, "Recovered")
	if err != nil {
		t.Fatal("deadline retained guard/slot", err)
	}
}

func TestAgentServiceDoesNotTruncateOverCapacityDrift(t *testing.T) {
	f := agentServiceFixture(t, false)
	fillAgentSlots(t, f, 50)
	// Structural probe does not certify CHECK expressions. If an operator
	// corrupts its body while preserving the name, List still must not truncate.
	execSQL(t, f.pool, "ALTER TABLE tenant_agents DROP CONSTRAINT tenant_agents_slot; ALTER TABLE tenant_agents ADD CONSTRAINT tenant_agents_slot CHECK(registry_slot>0)")
	execSQL(t, f.pool, "INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by) VALUES($1,$2,51,'Drift',$3)", uuid.NewString(), f.f.org, f.f.user)
	items, err := f.svc.List(context.Background(), f.p, f.f.org)
	serviceError(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if items != nil {
		t.Fatal("over-capacity list silently truncated")
	}
}
