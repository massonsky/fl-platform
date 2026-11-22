//go:build integration

package tenantagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func rejectsSQL(t *testing.T, pool *pgxpool.Pool, code, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, query, args...)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != code {
		t.Fatalf("expected SQLSTATE %s, got %v", code, err)
	}
}

func seedCredentialRows(t *testing.T, pool *pgxpool.Pool, f agentFixture) {
	t.Helper()
	// CTE samples one DB clock instant, rather than two subtly different clocks.
	execSQL(t, pool, `WITH instant AS (SELECT clock_timestamp() n)
 INSERT INTO agent_enrollments(agent_id,organization_id,enrollment_id,digest,issuer_user_id,generation,budget_window,created_at,expires_at)
 SELECT $1,$2,$3,decode(repeat('01',32),'hex'),$4,1,date_trunc('hour',n AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',n,n+interval '15 minutes' FROM instant`, f.agent, f.org, uuid.NewString(), f.user)
	execSQL(t, pool, `WITH instant AS (SELECT clock_timestamp() n)
 INSERT INTO agent_credentials(agent_id,organization_id,credential_id,digest,generation,created_at,expires_at)
 SELECT $1,$2,$3,decode(repeat('02',32),'hex'),1,n,n+interval '720 hours' FROM instant`, f.agent, f.org, uuid.NewString())
	execSQL(t, pool, `WITH instant AS (SELECT clock_timestamp() n)
 INSERT INTO agent_issue_budgets(organization_id,window_started_at,used_units,updated_at)
 SELECT $1,date_trunc('hour',n AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',2,n FROM instant`, f.org)
	execSQL(t, pool, `INSERT INTO agent_events(organization_id,agent_id,actor_kind,actor_user_id,action,agent_revision,after_state)
 VALUES($1,$2,'user',$3,'agent_created',1,'pending')`, f.org, f.agent, f.user)
}

func TestTenantAgentIdentityLimitsAndImmutableOwnership(t *testing.T) {
	pool := agentDatabase(t, true)
	f := seedAgentScope(t, pool, true)
	// The same human can belong to both orgs; ownership must still be immutable.
	execSQL(t, pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'admin')", f.otherOrg, f.user)
	for _, field := range []string{"organization_id", "id", "created_by"} {
		replacement := f.otherOrg
		if field == "id" {
			replacement = uuid.NewString()
		} else if field == "created_by" {
			replacement = f.otherUser
		}
		rejectsSQL(t, pool, "23514", "UPDATE tenant_agents SET "+field+"=$2 WHERE id=$1", f.agent, replacement)
	}
	for _, change := range []string{"registry_slot=0", "registry_slot=51", "revision=0", "name=''", "name=repeat('я',121)", "name=E'bad\\nname'", "state='revoked'", "updated_at='infinity'"} {
		rejectsSQL(t, pool, "23514", "UPDATE tenant_agents SET "+change+" WHERE id=$1", f.agent)
	}
	execSQL(t, pool, `INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by)
 SELECT gen_random_uuid(),$1,n,'Node',$2 FROM generate_series(2,50) n`, f.org, f.user)
	rejectsSQL(t, pool, "23505", "INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by) VALUES($1,$2,1,'Duplicate',$3)", uuid.NewString(), f.org, f.user)
	rejectsSQL(t, pool, "23514", "INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by) VALUES($1,$2,51,'Over cap',$3)", uuid.NewString(), f.org, f.user)
	// Another org has its own fifty slots; this slot is never a dataset index.
	execSQL(t, pool, "INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by) VALUES($1,$2,1,'Other org',$3)", uuid.NewString(), f.otherOrg, f.otherUser)
	if scalar(t, pool, "SELECT count(*)::text FROM tenant_agents WHERE organization_id=$1", f.org) != "50" {
		t.Fatal("per-organization identity bound failed")
	}
}

func TestTenantAgentCredentialScopeAndSafeAudit(t *testing.T) {
	pool := agentDatabase(t, true)
	f := seedAgentScope(t, pool, true)
	seedCredentialRows(t, pool, f)
	for _, table := range []string{"agent_enrollments", "agent_credentials", "agent_events"} {
		rejectsSQL(t, pool, "23503", "UPDATE "+table+" SET organization_id=$2 WHERE agent_id=$1", f.agent, f.otherOrg)
	}
	rejectsSQL(t, pool, "23503", "UPDATE agent_enrollments SET issuer_user_id=$2 WHERE agent_id=$1", f.agent, f.otherUser)
	// A revoked/suspended membership remains a metadata row: the FK must not
	// obstruct revocation. A1V must recheck its current status before exchange.
	execSQL(t, pool, "UPDATE organization_memberships SET status='revoked',revision=revision+1 WHERE organization_id=$1 AND user_id=$2", f.org, f.user)
	if scalar(t, pool, "SELECT count(*)::text FROM agent_enrollments WHERE agent_id=$1", f.agent) != "1" {
		t.Fatal("membership revoke deleted enrollment metadata")
	}
	for _, change := range []string{"actor_kind='machine'", "actor_user_id=NULL", "action='print_secret'", "after_state='broken'", "agent_revision=0", "credential_generation=0", "created_at='infinity'"} {
		rejectsSQL(t, pool, "23514", "UPDATE agent_events SET "+change+" WHERE agent_id=$1", f.agent)
	}
	rejectsSQL(t, pool, "42703", "UPDATE agent_events SET payload_json='{}' WHERE agent_id=$1", f.agent)
	for _, table := range []string{"agent_enrollments", "agent_credentials"} {
		for _, change := range []string{"digest=decode('00','hex')", "generation=0", "expires_at=created_at", "expires_at='infinity'", "revoked_at=created_at-interval '1 second'"} {
			rejectsSQL(t, pool, "23514", "UPDATE "+table+" SET "+change+" WHERE agent_id=$1", f.agent)
		}
	}
	if err := schemaReady(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
}

func TestTenantAgentExpiryReservationAndTimezone(t *testing.T) {
	pool := agentDatabase(t, true)
	f := seedAgentScope(t, pool, true)
	seedCredentialRows(t, pool, f)
	for _, change := range []string{"reserved_units=1", "reserved_units=4", "budget_window=budget_window+interval '1 minute'", "budget_window=budget_window-interval '1 hour'",
		"expires_at=created_at+interval '15 minutes 1 second'", "consumed_at=expires_at", "consumed_at=created_at,revoked_at=created_at"} {
		rejectsSQL(t, pool, "23514", "UPDATE agent_enrollments SET "+change+" WHERE agent_id=$1", f.agent)
	}
	rejectsSQL(t, pool, "23514", "UPDATE agent_credentials SET expires_at=created_at+interval '720 hours 1 second' WHERE agent_id=$1", f.agent)
	for _, change := range []string{"used_units=1", "used_units=22", "used_units=-2", "window_started_at=window_started_at+interval '1 minute'", "updated_at='infinity'"} {
		rejectsSQL(t, pool, "23514", "UPDATE agent_issue_budgets SET "+change+" WHERE organization_id=$1", f.org)
	}
	// Finite absolute lifetimes remain identical across DST and non-UTC sessions.
	for _, zone := range []string{"UTC", "Europe/Berlin", "Pacific/Chatham"} {
		ctx := context.Background()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, "SELECT set_config('TimeZone',$1,true)", zone)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE agent_credentials SET created_at='2026-10-24 12:00:00+00',
 expires_at='2026-10-24 12:00:00+00'::timestamptz+interval '720 hours' WHERE agent_id=$1`, f.agent)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE agent_enrollments SET budget_window='2026-10-24 12:00:00+00',created_at='2026-10-24 12:59:59+00',
 expires_at='2026-10-24 13:14:59+00',consumed_at='2026-10-24 13:01:00+00' WHERE agent_id=$1`, f.agent)
		}
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatalf("timezone %s changed lifetime/reservation: %v", zone, err)
		}
	}
	// The schema is metadata only: an expired reservation is retained, not refunded.
	execSQL(t, pool, `UPDATE agent_enrollments SET budget_window='2026-01-01 00:00:00+00',created_at='2026-01-01 00:00:00+00',expires_at='2026-01-01 00:15:00+00' WHERE agent_id=$1`, f.agent)
	if scalar(t, pool, "SELECT used_units::text FROM agent_issue_budgets WHERE organization_id=$1", f.org) != "2" {
		t.Fatal("expiry silently refunded issuance credit")
	}
}

func TestTenantAgentOneCurrentIdentityAndUniqueSelectors(t *testing.T) {
	pool := agentDatabase(t, true)
	f := seedAgentScope(t, pool, true)
	seedCredentialRows(t, pool, f)
	second := uuid.NewString()
	execSQL(t, pool, "INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by) VALUES($1,$2,2,'Second',$3)", second, f.org, f.user)
	for _, table := range []string{"agent_enrollments", "agent_credentials"} {
		rejectsSQL(t, pool, "23505", "INSERT INTO "+table+" SELECT * FROM "+table+" WHERE agent_id=$1", f.agent)
	}
	base := `INSERT INTO agent_credentials(agent_id,organization_id,credential_id,digest,generation,created_at,expires_at)
 SELECT $2,organization_id,credential_id,decode(repeat('03',32),'hex'),1,created_at,expires_at FROM agent_credentials WHERE agent_id=$1`
	rejectsSQL(t, pool, "23505", base, f.agent, second)
	rejectsSQL(t, pool, "23505", `INSERT INTO agent_credentials(agent_id,organization_id,credential_id,digest,generation,created_at,expires_at)
 SELECT $2,organization_id,gen_random_uuid(),digest,1,created_at,expires_at FROM agent_credentials WHERE agent_id=$1`, f.agent, second)
	rejectsSQL(t, pool, "23505", `INSERT INTO agent_enrollments(agent_id,organization_id,enrollment_id,digest,issuer_user_id,generation,budget_window,reserved_units,created_at,expires_at)
 SELECT $2,organization_id,enrollment_id,decode(repeat('03',32),'hex'),issuer_user_id,1,budget_window,2,created_at,expires_at FROM agent_enrollments WHERE agent_id=$1`, f.agent, second)
	if scalar(t, pool, "SELECT count(*)::text FROM agent_credentials") != "1" || scalar(t, pool, "SELECT count(*)::text FROM agent_enrollments") != "1" {
		t.Fatal("failed insert changed current credential state")
	}
}
