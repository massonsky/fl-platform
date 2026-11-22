//go:build integration

package tenantagent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

type serviceFixture struct {
	pool     *pgxpool.Pool
	svc      Service
	f        agentFixture
	p, other identity.Principal
}

func agentServiceFixture(t *testing.T, withAgent bool) serviceFixture {
	t.Helper()
	pool := agentDatabase(t, true)
	f := seedAgentScope(t, pool, withAgent)
	return serviceFixture{pool, Service{Pool: pool}, f, agentSession(t, pool, f.user), agentSession(t, pool, f.otherUser)}
}

func agentSession(t *testing.T, pool *pgxpool.Pool, id string) identity.Principal {
	t.Helper()
	hash := identity.Hash("tenant-agent-fixture/" + id)
	execSQL(t, pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')", hash, id)
	return identity.Principal{UserID: id, SessionHash: hash}
}

func serviceError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var f Failure
	if !errors.As(err, &f) || f.Status != status || f.Code != code {
		t.Fatalf("want %d %s, got %v", status, code, err)
	}
	if status == 503 && f.RetryAfter != 3 {
		t.Fatal("dependency error must be retryable without SQL details", f)
	}
}

func denyService(t *testing.T, f serviceFixture, p identity.Principal, org string, status int, code string) {
	t.Helper()
	ctx := context.Background()
	items, err := f.svc.List(ctx, p, org)
	serviceError(t, err, status, code)
	if items != nil {
		t.Fatal("failed list leaked metadata")
	}
	a, err := f.svc.Create(ctx, p, org, f.f.agent, "Node")
	serviceError(t, err, status, code)
	if a != (Agent{}) {
		t.Fatal("failed create leaked metadata")
	}
	d, err := f.svc.Get(ctx, p, org, f.f.agent)
	serviceError(t, err, status, code)
	if !reflect.DeepEqual(d, Details{}) {
		t.Fatal("failed detail leaked metadata")
	}
}

func holdAgentGuard(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if err = authorization.Write(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	return tx
}

func waitAgentGuard(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
 WHERE a.datname=current_database() AND l.locktype='advisory' AND NOT l.granted)`).Scan(&waiting)
		if err != nil {
			t.Fatal("guard waiter not observed", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("guard waiter not observed")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func fillAgentSlots(t *testing.T, f serviceFixture, count int) {
	t.Helper()
	execSQL(t, f.pool, `INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by)
 SELECT gen_random_uuid(),$1,slot,'Existing '||slot,$2 FROM generate_series(1,$3::integer) slot`, f.f.org, f.f.user, count)
}

func agentState(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	return relationData(t, pool, []string{"tenant_agents", "agent_events", "agent_enrollments", "agent_credentials", "agent_issue_budgets"})
}

func unchangedAgentState(t *testing.T, pool *pgxpool.Pool, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, agentState(t, pool)) {
		t.Fatal("unexpected persisted agent effect")
	}
}
