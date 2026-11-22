//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
)

func TestOrganizationContract(t *testing.T) {
	pool := aiDatabase(t) // Existing helper creates a disposable database with 1..6.
	raw, err := os.ReadFile("/src/db/migrations/000007_organizations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = pool.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	users, tokens := map[string]string{}, map[string]string{}
	for name, role := range map[string]string{"owner": "researcher", "foreign": "admin", "viewer": "viewer", "member": "researcher"} {
		id, token := uuid.NewString(), identity.Token()
		users[name], tokens[name] = id, token
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,external_subject,display_name,role) VALUES($1,$2,$3,$4)`, id, id, name, role); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')`, identity.Hash(token), id); err != nil {
			t.Fatal(err)
		}
	}
	s := Server{Identity: identity.Store{Pool: pool}, Organizations: &organization.Service{Pool: pool}}
	call := func(user, method, path, input, media string, want int, code string) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/"+path, strings.NewReader(input))
		if token := tokens[user]; token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if media != "" {
			r.Header.Set("Content-Type", media)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s (%s): got %d want %d: %s", method, path, user, w.Code, want, w.Body.String())
		}
		if code != "" {
			var failure struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Code != code {
				t.Fatalf("want %s: %s", code, w.Body.String())
			}
			if w.Header().Get("X-Request-ID") == "" {
				t.Fatal("error has no request ID")
			}
		}
		return w.Body.Bytes()
	}
	jsonCall := func(user, method, path, input string, want int, code string) []byte {
		t.Helper()
		return call(user, method, path, input, "application/json", want, code)
	}
	id := uuid.NewString()
	path := "organizations/" + id
	jsonCall("", "GET", "organizations", "", 401, "UNAUTHORIZED")
	if string(jsonCall("owner", "GET", "organizations", "", 200, "")) != "{\"items\":[]}\n" {
		t.Fatal("new owner list is not empty")
	}
	var created organization.Organization
	if json.Unmarshal(jsonCall("owner", "PUT", path, `{"name":"  Research team  "}`, 200, ""), &created) != nil ||
		created.ID != id || created.Name != "Research team" || created.Role != "owner" || !created.CanManageMembers {
		t.Fatal("invalid organization DTO", created)
	}
	jsonCall("owner", "PUT", path, `{"name":"Research team"}`, 200, "")
	jsonCall("owner", "PUT", path, `{"name":"Changed"}`, 409, "VERSION_CONFLICT")
	jsonCall("foreign", "PUT", path, `{"name":"Research team"}`, 404, "NOT_FOUND")
	jsonCall("viewer", "PUT", "organizations/"+uuid.NewString(), `{"name":"No write"}`, 403, "FORBIDDEN")
	jsonCall("foreign", "GET", path+"/members", "", 404, "NOT_FOUND")
	jsonCall("foreign", "PATCH", path+"/members/"+users["owner"], `{"role":"viewer","status":"active","revision":1}`, 404, "NOT_FOUND")
	if string(jsonCall("foreign", "GET", "organizations", "", 200, "")) != "{\"items\":[]}\n" {
		t.Fatal("platform administrator bypassed membership")
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM organizations WHERE id=$1", id).Scan(&count); err != nil || count != 1 {
		t.Fatal("UUID replay duplicated organization", count, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'researcher'),($1,$3,'admin')`, id, users["member"], users["foreign"]); err != nil {
		t.Fatal(err)
	}
	memberPath, ownerPath := path+"/members/"+users["member"], path+"/members/"+users["owner"]
	change := `{"role":"viewer","status":"suspended","revision":1}`
	jsonCall("member", "GET", path+"/members", "", 403, "FORBIDDEN")
	jsonCall("foreign", "PATCH", ownerPath, change, 403, "FORBIDDEN")
	jsonCall("foreign", "PATCH", memberPath, `{"role":"owner","status":"active","revision":1}`, 403, "FORBIDDEN")
	var member organization.Membership
	if json.Unmarshal(jsonCall("owner", "PATCH", memberPath, change, 200, ""), &member) != nil || member.Revision != 2 || member.Status != "suspended" {
		t.Fatal("member mutation failed", member)
	}
	jsonCall("owner", "PATCH", memberPath, change, 409, "VERSION_CONFLICT")
	jsonCall("member", "GET", path+"/members", "", 404, "NOT_FOUND")
	jsonCall("owner", "PATCH", ownerPath, `{"role":"researcher","status":"active","revision":1}`, 409, "LAST_OWNER")
	jsonCall("owner", "PATCH", path+"/members/"+uuid.NewString(), change, 404, "NOT_FOUND")
	jsonCall("owner", "GET", path+"/members", "", 200, "")
	for _, invalid := range []string{`{"role":"viewer","status":"active"}`, `{"role":"viewer","status":"active","revision":1.5}`, `{"role":"viewer","status":"active","revision":9007199254740992}`} {
		jsonCall("owner", "PATCH", memberPath, invalid, 400, "INVALID_REQUEST")
	}
	for _, invalid := range []string{`{}`, `{"name":"x","owner_id":"other"}`, `{"name":"x","name":"y"}`, `{"name":"x"} {}`, `{"name":"\u0000"}`} {
		jsonCall("owner", "PUT", "organizations/"+uuid.NewString(), invalid, 400, "INVALID_REQUEST")
	}
	jsonCall("owner", "PUT", "organizations/not-a-uuid", `{"name":"x"}`, 400, "INVALID_REQUEST")
	jsonCall("owner", "PUT", "organizations/"+uuid.Nil.String(), `{"name":"x"}`, 400, "INVALID_REQUEST")
	jsonCall("owner", "PUT", path, strings.Repeat("x", 65537), 413, "PAYLOAD_TOO_LARGE")
	call("owner", "PUT", path, `{"name":"x"}`, "text/plain", 415, "UNSUPPORTED_MEDIA_TYPE")
	jsonCall("owner", "GET", path, "", 404, "NOT_FOUND")
	jsonCall("owner", "POST", path+"/members", "{}", 404, "NOT_FOUND")
	// Authentication uses DB state, not a role supplied by a client or stale UI.
	if _, err = pool.Exec(ctx, "UPDATE users SET role='viewer' WHERE id=$1", users["owner"]); err != nil {
		t.Fatal(err)
	}
	jsonCall("owner", "PATCH", memberPath, `{"role":"researcher","status":"active","revision":2}`, 403, "FORBIDDEN")
	jsonCall("owner", "GET", path+"/members", "", 200, "")
	var page struct {
		Items []organization.Organization `json:"items"`
	}
	if json.Unmarshal(jsonCall("owner", "GET", "organizations", "", 200, ""), &page) != nil || len(page.Items) != 1 || page.Items[0].CanManageMembers {
		t.Fatal("legacy viewer gained write capability")
	}
	if _, err = pool.Exec(ctx, "UPDATE organizations SET state='suspended' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	jsonCall("foreign", "PATCH", memberPath, `{"role":"researcher","status":"active","revision":2}`, 403, "FORBIDDEN")
	jsonCall("foreign", "GET", path+"/members", "", 200, "")
	s.Organizations = nil
	jsonCall("owner", "GET", "organizations", "", 503, "ORGS_NOT_CONFIGURED")
	jsonCall("", "GET", "organizations", "", 401, "UNAUTHORIZED")
}
