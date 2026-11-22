//go:build integration

package tenantagent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func TestAgentServiceScopeAndCurrentIdentity(t *testing.T) {
	f := agentServiceFixture(t, true)
	before := agentState(t, f.pool)
	for _, global := range []string{"admin", "researcher", "viewer"} {
		execSQL(t, f.pool, "UPDATE users SET role=$2 WHERE id=$1", f.f.otherUser, global)
		denyService(t, f, f.other, f.f.org, 404, "NOT_FOUND")
	}
	for _, role := range []string{"owner", "admin", "researcher", "viewer"} {
		execSQL(t, f.pool, "UPDATE organization_memberships SET role=$3 WHERE organization_id=$1 AND user_id=$2", f.f.org, f.f.user, role)
		if role == "owner" || role == "admin" {
			for _, global := range []string{"admin", "researcher"} {
				execSQL(t, f.pool, "UPDATE users SET role=$2 WHERE id=$1", f.f.user, global)
				items, err := f.svc.List(context.Background(), f.p, f.f.org)
				if err != nil || len(items) != 1 {
					t.Fatal("manager read", role, global, err)
				}
			}
		} else {
			denyService(t, f, f.p, f.f.org, 403, "FORBIDDEN")
		}
	}
	execSQL(t, f.pool, "UPDATE users SET role='viewer' WHERE id=$1", f.f.user)
	execSQL(t, f.pool, "UPDATE organization_memberships SET role='owner' WHERE organization_id=$1", f.f.org)
	denyService(t, f, f.p, f.f.org, 403, "FORBIDDEN")
	for _, status := range []string{"suspended", "revoked"} {
		execSQL(t, f.pool, "UPDATE organization_memberships SET status=$2 WHERE organization_id=$1", f.f.org, status)
		denyService(t, f, f.p, f.f.org, 404, "NOT_FOUND") // Presence precedes global viewer cap.
	}
	execSQL(t, f.pool, "UPDATE organization_memberships SET status='active' WHERE organization_id=$1", f.f.org)
	execSQL(t, f.pool, "UPDATE users SET role='researcher' WHERE id=$1", f.f.user)
	execSQL(t, f.pool, "UPDATE organizations SET state='suspended' WHERE id=$1", f.f.org)
	denyService(t, f, f.p, f.f.org, 403, "FORBIDDEN")
	execSQL(t, f.pool, "UPDATE organizations SET state='active',kind='personal',personal_owner_id=$2 WHERE id=$1", f.f.org, f.f.user)
	denyService(t, f, f.p, f.f.org, 403, "FORBIDDEN")
	execSQL(t, f.pool, "UPDATE users SET disabled_at=clock_timestamp() WHERE id=$1", f.f.user)
	denyService(t, f, f.p, f.f.org, 401, "UNAUTHORIZED")
	execSQL(t, f.pool, "UPDATE users SET disabled_at=NULL WHERE id=$1", f.f.user)
	if err := (identity.Store{Pool: f.pool}).Logout(context.Background(), "tenant-agent-fixture/"+f.f.user); err != nil {
		t.Fatal(err)
	}
	denyService(t, f, f.p, f.f.org, 401, "UNAUTHORIZED")
	unchangedAgentState(t, f.pool, before)
}

func TestAgentServiceCreateReplayAndBoundedList(t *testing.T) {
	f := agentServiceFixture(t, false)
	ctx := context.Background()
	items, err := f.svc.List(ctx, f.p, f.f.org)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatal("empty bounded list", items, err)
	}
	seedLegacy(t, f.pool, f.f)
	legacy := []string{"users", "sessions", "organizations", "organization_memberships", "projects", "experiments", "runs", "rounds", "clients", "tasks", "http_receipts"}
	old := relationData(t, f.pool, legacy)
	a, err := f.svc.Create(ctx, f.p, f.f.org, f.f.agent, "  Узел организации  ")
	if err != nil || a.Name != "Узел организации" || a.State != StatePending || a.Revision != 1 || a.CreatedBy != f.f.user {
		t.Fatal("create", a, err)
	}
	if !reflect.DeepEqual(old, relationData(t, f.pool, legacy)) {
		t.Fatal("legacy/organization data changed")
	}
	execSQL(t, f.pool, "UPDATE tenant_agents SET state='disabled',revision=2147483647 WHERE id=$1", a.ID)
	execSQL(t, f.pool, `INSERT INTO tenant_agents(id,organization_id,registry_slot,name,created_by)
 SELECT gen_random_uuid(),$1,slot,'Other '||slot,$2 FROM generate_series(2,50) slot`, f.f.org, f.f.user)
	execSQL(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'admin')", f.f.org, f.f.otherUser)
	before := agentState(t, f.pool)
	a, err = f.svc.Create(ctx, f.other, f.f.org, a.ID, a.Name)
	if err != nil || a.State != StateDisabled || a.Revision != 2147483647 || a.CreatedBy != f.f.user {
		t.Fatal("current replay", a, err)
	}
	_, err = f.svc.Create(ctx, f.p, f.f.org, a.ID, "different")
	serviceError(t, err, 409, "VERSION_CONFLICT")
	_, err = f.svc.Create(ctx, f.p, f.f.org, uuid.NewString(), "new")
	serviceError(t, err, 409, "AGENT_LIMIT")
	_, err = f.svc.Create(ctx, f.other, f.f.otherOrg, a.ID, a.Name)
	serviceError(t, err, 404, "NOT_FOUND")
	unchangedAgentState(t, f.pool, before)
	// Equal timestamps exercise the deterministic UUID tie-breaker.
	execSQL(t, f.pool, "UPDATE tenant_agents SET created_at='2020-01-01 00:00:00+00' WHERE organization_id=$1", f.f.org)
	items, err = f.svc.List(ctx, f.p, f.f.org)
	if err != nil || len(items) != IdentityLimit {
		t.Fatal("full list", len(items), err)
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].ID >= items[i].ID {
			t.Fatal("unstable order")
		}
	}
	if scalar(t, f.pool, "SELECT count(*)::text FROM agent_events") != "1" {
		t.Fatal("replay emitted extra audit")
	}
}

func TestAgentServiceMetadataAndValidation(t *testing.T) {
	f := agentServiceFixture(t, true)
	ctx := context.Background()
	d, err := f.svc.Get(ctx, f.p, f.f.org, f.f.agent)
	if err != nil || d.Enrollment != nil || d.Credential != nil {
		t.Fatal("optional metadata", d, err)
	}
	seedCredentialRows(t, f.pool, f.f)
	execSQL(t, f.pool, "UPDATE agent_credentials SET revoked_at=clock_timestamp() WHERE agent_id=$1", f.f.agent)
	execSQL(t, f.pool, "UPDATE agent_enrollments SET created_at=created_at-interval '1 hour',budget_window=budget_window-interval '1 hour',expires_at=expires_at-interval '1 hour' WHERE agent_id=$1", f.f.agent)
	d, err = f.svc.Get(ctx, f.p, f.f.org, f.f.agent)
	if err != nil || d.Enrollment == nil || d.Credential == nil || d.Credential.RevokedAt == nil {
		t.Fatal("diagnostic metadata", d, err)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"digest", "registry_slot", "secret", "token", "01010101", "02020202"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("metadata leak", forbidden)
		}
	}
	for _, bad := range []string{"", "bad", uuid.Nil.String(), "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"} {
		_, err = f.svc.Get(ctx, f.p, f.f.org, bad)
		serviceError(t, err, 400, "INVALID_REQUEST")
		_, err = f.svc.List(ctx, f.p, bad)
		serviceError(t, err, 400, "INVALID_REQUEST")
	}
	for _, name := range []string{"", " \t", "nul\x00", strings.Repeat("я", 121)} {
		_, err = f.svc.Create(ctx, f.p, f.f.org, uuid.NewString(), name)
		serviceError(t, err, 400, "INVALID_REQUEST")
	}
	_, err = f.svc.Create(ctx, identity.Principal{}, "bad", "bad", "")
	serviceError(t, err, 401, "UNAUTHORIZED")
	_, err = f.svc.Get(ctx, f.p, f.f.org, uuid.NewString())
	serviceError(t, err, 404, "NOT_FOUND")
	_, err = f.svc.Get(ctx, f.other, f.f.otherOrg, f.f.agent)
	serviceError(t, err, 404, "NOT_FOUND")
}
