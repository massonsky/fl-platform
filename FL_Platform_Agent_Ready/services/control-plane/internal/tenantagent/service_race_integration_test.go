//go:build integration

package tenantagent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type createResult struct {
	agent Agent
	err   error
}

func TestAgentServiceConcurrentUUIDAndLastSlot(t *testing.T) {
	t.Run("same UUID", func(t *testing.T) {
		f := agentServiceFixture(t, false)
		results := make(chan createResult, 8)
		for i := 0; i < cap(results); i++ {
			go func() {
				a, err := f.svc.Create(context.Background(), f.p, f.f.org, f.f.agent, "Same node")
				results <- createResult{a, err}
			}()
		}
		var first Agent
		for i := 0; i < cap(results); i++ {
			r := <-results
			if r.err != nil || r.agent.ID != f.f.agent {
				t.Fatal("concurrent UUID", r)
			}
			if i == 0 {
				first = r.agent
			} else if first != r.agent {
				t.Fatal("replay changed metadata")
			}
		}
		if scalar(t, f.pool, "SELECT count(*)::text FROM tenant_agents") != "1" || scalar(t, f.pool, "SELECT count(*)::text FROM agent_events") != "1" {
			t.Fatal("duplicate create/audit")
		}
	})
	t.Run("last slot", func(t *testing.T) {
		f := agentServiceFixture(t, false)
		fillAgentSlots(t, f, 49)
		results := make(chan createResult, 2)
		for i := 0; i < 2; i++ {
			go func() {
				a, err := f.svc.Create(context.Background(), f.p, f.f.org, uuid.NewString(), "Last node")
				results <- createResult{a, err}
			}()
		}
		accepted := 0
		for i := 0; i < 2; i++ {
			r := <-results
			if r.err == nil {
				accepted++
			} else {
				serviceError(t, r.err, 409, "AGENT_LIMIT")
				if r.agent != (Agent{}) {
					t.Fatal("rejected capacity returned metadata")
				}
			}
		}
		if accepted != 1 || scalar(t, f.pool, "SELECT count(*)::text FROM tenant_agents") != "50" || scalar(t, f.pool, "SELECT count(*)::text FROM agent_events") != "1" {
			t.Fatal("capacity race")
		}
	})
}

func TestAgentServiceWaitersObserveRevocationAndExpiry(t *testing.T) {
	for _, mode := range []string{"logout", "global role", "membership", "org suspension", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			f := agentServiceFixture(t, false)
			before := agentState(t, f.pool)
			tx := holdAgentGuard(t, f.pool)
			result := make(chan createResult, 1)
			go func() {
				a, err := f.svc.Create(context.Background(), f.p, f.f.org, f.f.agent, "Waiting node")
				result <- createResult{a, err}
			}()
			waitAgentGuard(t, f.pool)
			query, args, status, code := "", []any{}, 401, "UNAUTHORIZED"
			switch mode {
			case "logout":
				query = "DELETE FROM sessions WHERE token_hash=$1"
				args = []any{f.p.SessionHash}
			case "global role":
				query = "UPDATE users SET role='viewer' WHERE id=$1"
				args = []any{f.f.user}
				status = 403
				code = "FORBIDDEN"
			case "membership":
				query = "UPDATE organization_memberships SET status='revoked' WHERE organization_id=$1 AND user_id=$2"
				args = []any{f.f.org, f.f.user}
				status = 404
				code = "NOT_FOUND"
			case "org suspension":
				query = "UPDATE organizations SET state='suspended' WHERE id=$1"
				args = []any{f.f.org}
				status = 403
				code = "FORBIDDEN"
			case "expiry":
				query = "UPDATE sessions SET expires_at=clock_timestamp()+interval '100 milliseconds' WHERE token_hash=$1"
				args = []any{f.p.SessionHash}
			}
			if _, err := tx.Exec(context.Background(), query, args...); err != nil {
				t.Fatal(err)
			}
			// Waiter transaction began before this future timestamp. now() would
			// wrongly admit it; Current must sample clock_timestamp after the wait.
			if mode == "expiry" {
				if _, err := tx.Exec(context.Background(), "SELECT pg_sleep(0.2)"); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			r := <-result
			serviceError(t, r.err, status, code)
			if r.agent != (Agent{}) {
				t.Fatal("waiter leaked uncommitted metadata")
			}
			unchangedAgentState(t, f.pool, before)
		})
	}
}

func TestAgentServiceReadWaiterAndCancellation(t *testing.T) {
	f := agentServiceFixture(t, true)
	tx := holdAgentGuard(t, f.pool)
	result := make(chan error, 1)
	go func() { _, err := f.svc.Get(context.Background(), f.p, f.f.org, f.f.agent); result <- err }()
	waitAgentGuard(t, f.pool)
	if _, err := tx.Exec(context.Background(), "UPDATE organization_memberships SET role='viewer' WHERE organization_id=$1 AND user_id=$2", f.f.org, f.f.user); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	serviceError(t, <-result, 403, "FORBIDDEN")
	execSQL(t, f.pool, "UPDATE organization_memberships SET role='owner' WHERE organization_id=$1", f.f.org)
	tx = holdAgentGuard(t, f.pool)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	a, err := f.svc.Create(ctx, f.p, f.f.org, uuid.NewString(), "Cancelled")
	serviceError(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if a != (Agent{}) {
		t.Fatal("cancelled guard leaked result")
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Create(context.Background(), f.p, f.f.org, uuid.NewString(), "After cancellation")
	if err != nil {
		t.Fatal("cancelled request retained lock", err)
	}
}
