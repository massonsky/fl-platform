//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
)

func organizationPrincipal(f authorizationFixture, name string) identity.Principal {
	return identity.Principal{UserID: f.users[name].ID, SessionHash: identity.Hash(f.users[name].ID)}
}
func organizationFailure(t *testing.T, err error, status int, code string) {
	t.Helper()
	var f organization.Failure
	if !errors.As(err, &f) || f.Status != status || f.Code != code {
		t.Fatalf("got%v want%d %s", err, status, code)
	}
}
func organizationSessionHTTP(t *testing.T, f authorizationFixture, p identity.Principal, method, path, input string, status int, code string) {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/"+path, strings.NewReader(input))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.s.organizations(w, r, p, path) // Principal was captured before the security change.
	var response struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	}
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Code != code {
		t.Fatalf("%s: got%d %s", path, w.Code, w.Body.String())
	}
	if status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing challenge")
	}
	if status == 503 && (!response.Retryable || w.Header().Get("Retry-After") != "3" || w.Header().Get("WWW-Authenticate") != "") {
		t.Fatal("dependency retry headers")
	}
}

func TestOrganizationSessionRevocationAndReplay(t *testing.T) {
	for _, change := range []string{"logout", "password", "disable"} {
		t.Run(change, func(t *testing.T) {
			f := authorizationDB(t, true)
			var credentialService admin.Service
			if change == "password" {
				credentialService = configureCredentialAdministration(t, f.s)
			}
			f.s.Organizations = &organization.Service{Pool: f.pool}
			ctx := context.Background()
			p := organizationPrincipal(f, "owner")
			id := uuid.NewString()
			first, err := f.s.Organizations.Create(ctx, p, id, "Session fixture")
			if err != nil || first.ID != id {
				t.Fatal(first, err)
			}
			if _, err = f.s.Organizations.Create(ctx, p, id, " Session fixture "); err != nil {
				t.Fatal("valid replay", err)
			}
			items, err := f.s.Organizations.List(ctx, p)
			if err != nil || len(items) != 1 || !items[0].CanManageMembers {
				t.Fatal(items, err)
			}
			// A second active owner permits global disable without orphaning a shared org.
			authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", id, f.users["other"].ID)
			bad := p
			bad.SessionHash = identity.Hash(f.users["other"].ID)
			_, err = f.s.Organizations.List(ctx, bad)
			organizationFailure(t, err, 401, "UNAUTHORIZED")
			store := admin.Store{Pool: f.pool}
			switch change {
			case "logout":
				err = f.s.Identity.Logout(ctx, f.users["owner"].ID)
			case "password":
				_, err = credentialService.MutateCredential(ctx, p, administrationCredentialCommand("password.change", "", "org-password", strings.Repeat("e", 64), admin.Input{CurrentPassword: "valid-old-password", Password: "valid-new-password"}))
			case "disable":
				disabled := true
				_, err = store.Mutate(ctx, f.users["admin"].ID, "user.update", p.UserID, "org-disable", strings.Repeat("f", 64), admin.Input{Role: "researcher", Disabled: &disabled})
			}
			if err != nil {
				t.Fatal(err)
			}
			items, err = f.s.Organizations.List(ctx, p)
			organizationFailure(t, err, 401, "UNAUTHORIZED")
			if items != nil {
				t.Fatal("unauthorized DTO")
			}
			created, err := f.s.Organizations.Create(ctx, p, id, "Session fixture")
			organizationFailure(t, err, 401, "UNAUTHORIZED")
			if created.ID != "" {
				t.Fatal("replay returned resource after revoke")
			}
			members, err := f.s.Organizations.ListMembers(ctx, p, id)
			organizationFailure(t, err, 401, "UNAUTHORIZED")
			if members != nil {
				t.Fatal("revoked membership list")
			}
			member, err := f.s.Organizations.ChangeMember(ctx, p, id, f.users["other"].ID, organization.MemberChange{Role: "viewer", Status: "active", Revision: 1})
			organizationFailure(t, err, 401, "UNAUTHORIZED")
			if member.UserID != "" {
				t.Fatal("denied write returned DTO")
			}
			organizationSessionHTTP(t, f, p, "PUT", "organizations/"+id, `{"name":"Session fixture"}`, 401, "UNAUTHORIZED")
			if authorizationValue(t, f.pool, "SELECT count(*)::text FROM organization_events") != "1" {
				t.Fatal("revoked request wrote audit")
			}
			if _, err = f.s.Organizations.ListMembers(ctx, organizationPrincipal(f, "other"), id); err != nil {
				t.Fatal("unrelated session lost access", err)
			}
		})
	}
}

func TestOrganizationSessionSchemaAndDatabaseFailures(t *testing.T) {
	f := authorizationDB(t, false)
	f.s.Organizations = &organization.Service{Pool: f.pool}
	p := organizationPrincipal(f, "owner")
	ctx := context.Background()
	_, err := f.s.Organizations.List(ctx, p)
	organizationFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	_, err = f.s.Organizations.List(ctx, identity.Principal{})
	organizationFailure(t, err, 401, "UNAUTHORIZED")
	// Migration7 backfills only owners with existing legacy projects.
	authorizationExec(t, f.pool, "INSERT INTO projects(id,owner_id,slug,name) VALUES($1,$2,'before-organizations','Legacy before schema7')", uuid.NewString(), p.UserID)
	raw, err := os.ReadFile("/src/db/migrations/000007_organizations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	authorizationExec(t, f.pool, string(raw))
	items, err := f.s.Organizations.List(ctx, p)
	if err != nil || len(items) != 1 {
		t.Fatal("schema7 personal backfill", items, err)
	}
	id := uuid.NewString()
	if _, err = f.s.Organizations.Create(ctx, p, id, "Schema fixture"); err != nil {
		t.Fatal(err)
	}
	authorizationExec(t, f.pool, "ALTER TABLE organization_events RENAME TO missing_organization_events")
	organizationSessionHTTP(t, f, p, "GET", "organizations", "", 503, "DEPENDENCY_UNAVAILABLE")
	_, err = f.s.Organizations.Create(ctx, p, id, "Schema fixture")
	organizationFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	authorizationExec(t, f.pool, "ALTER TABLE missing_organization_events RENAME TO organization_events")
	// Existing table with a broken query dependency must also be503, not empty/404.
	authorizationExec(t, f.pool, "ALTER TABLE organizations RENAME COLUMN name TO unavailable_name")
	organizationSessionHTTP(t, f, p, "GET", "organizations", "", 503, "DEPENDENCY_UNAVAILABLE")
}
