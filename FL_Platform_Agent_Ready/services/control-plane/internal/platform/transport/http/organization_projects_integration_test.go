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
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

type projectHTTPFixture struct {
	authorizationFixture
	org, otherOrg string
}

func projectHTTPDB(t *testing.T) projectHTTPFixture {
	t.Helper()
	f := projectHTTPFixture{authorizationFixture: authorizationDB(t, true), org: uuid.NewString(), otherOrg: uuid.NewString()}
	raw, err := os.ReadFile("/src/db/migrations/000008_project_access.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	authorizationExec(t, f.pool, string(raw))
	f.s.ProjectAccess = &projectaccess.Service{Pool: f.pool}
	f.s.Organizations = &organization.Service{Pool: f.pool}
	f.s.Experiments = experiment.Store{Pool: f.pool}
	for org, owner := range map[string]string{f.org: f.users["owner"].ID, f.otherOrg: f.users["admin"].ID} {
		if _, err := (organization.Store{Pool: f.pool}).Create(context.Background(), owner, org, "HTTP project fixture"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"other", "viewer"} {
		authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'researcher')", f.org, f.users[name].ID)
	}
	return f
}
func (f projectHTTPFixture) call(t *testing.T, actor, method, path, input string, status int, code string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/"+path, strings.NewReader(input))
	if user, ok := f.users[actor]; ok {
		r.Header.Set("Authorization", "Bearer "+user.ID)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "project-http-key")
	w := httptest.NewRecorder()
	f.s.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s (%s): got%d want%d: %s", method, path, actor, w.Code, status, w.Body.String())
	}
	if code != "" {
		var value struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(w.Body.Bytes(), &value) != nil || value.Code != code {
			t.Fatalf("want%s: %s", code, w.Body.String())
		}
		if w.Header().Get("X-Request-ID") == "" {
			t.Fatal("missing request ID")
		}
	}
	if status == 503 && (w.Header().Get("Retry-After") != "3" || w.Header().Get("WWW-Authenticate") != "") {
		t.Fatal("dependency response headers")
	}
	if status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing challenge")
	}
	return w
}
func (f projectHTTPFixture) base() string { return "organizations/" + f.org + "/projects" }
func (f projectHTTPFixture) create(t *testing.T, slug string) string {
	t.Helper()
	id := uuid.NewString()
	f.call(t, "owner", "PUT", f.base()+"/"+id, `{"slug":"`+slug+`","name":"Project"}`, 200, "")
	return id
}

func TestOrganizationProjectHTTPCreateAccessAndLegacyPrivacy(t *testing.T) {
	f := projectHTTPDB(t)
	base := f.base()
	f.call(t, "", "GET", base, "", 401, "UNAUTHORIZED")
	empty := f.call(t, "owner", "GET", base, "", 200, "")
	if !strings.Contains(empty.Body.String(), `"can_create_projects":true`) || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatal(empty.Body.String())
	}
	id := f.create(t, "shared-project")
	path := base + "/" + id
	f.call(t, "owner", "PUT", path, `{"slug":"shared-project","name":" Project "}`, 200, "")
	f.call(t, "owner", "PUT", path, `{"slug":"shared-project","name":"Different"}`, 409, "VERSION_CONFLICT")
	f.call(t, "owner", "PUT", base+"/"+uuid.NewString(), `{"slug":"shared-project","name":"Project"}`, 409, "SLUG_CONFLICT")
	f.call(t, "admin", "PUT", "organizations/"+f.otherOrg+"/projects/"+id, `{"slug":"shared-project","name":"Project"}`, 404, "NOT_FOUND")
	f.call(t, "viewer", "PUT", base+"/"+uuid.NewString(), `{"slug":"viewer-create","name":"Project"}`, 403, "FORBIDDEN")
	f.call(t, "other", "GET", path, "", 404, "NOT_FOUND")
	member := path + "/members/" + f.users["other"].ID
	f.call(t, "owner", "PUT", member, `{"role":"researcher","status":"active","revision":0}`, 200, "")
	f.call(t, "other", "GET", path, "", 200, "")
	f.call(t, "other", "GET", path+"/access", "", 403, "FORBIDDEN")
	f.call(t, "owner", "PUT", path+"/members/"+f.users["owner"].ID, `{"role":"viewer","status":"revoked","revision":0}`, 409, "INHERITED_ACCESS")
	f.call(t, "owner", "PATCH", path+"/visibility", `{"visibility":"organization","acl_revision":2}`, 200, "")
	f.call(t, "owner", "PATCH", path+"/visibility", `{"visibility":"private","acl_revision":2}`, 409, "VERSION_CONFLICT")
	f.call(t, "owner", "PUT", member, `{"role":"viewer","status":"revoked","revision":1}`, 200, "")
	f.call(t, "other", "GET", path, "", 404, "NOT_FOUND")
	f.call(t, "other", "GET", base, "", 200, "")
	legacy := f.call(t, "owner", "GET", "projects", "", 200, "")
	if strings.Contains(legacy.Body.String(), id) {
		t.Fatal("shared owner leaked through legacy list")
	}
	f.call(t, "owner", "POST", "projects/"+id+"/experiments", `{}`, 404, "NOT_FOUND")
	f.call(t, "owner", "GET", path+"/runs", "", 404, "NOT_FOUND")
	f.call(t, "owner", "GET", path+"/export", "", 404, "NOT_FOUND")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM runs") != "0" {
		t.Fatal("shared run created")
	}
	authorizationExec(t, f.pool, "UPDATE organizations SET state='suspended' WHERE id=$1", f.org)
	f.call(t, "owner", "GET", path+"/access", "", 200, "")
	f.call(t, "owner", "PATCH", path+"/visibility", `{"visibility":"private","acl_revision":4}`, 403, "FORBIDDEN")
	service := f.s.ProjectAccess
	f.s.ProjectAccess = nil
	f.call(t, "owner", "GET", base, "", 503, "ORG_PROJECTS_NOT_CONFIGURED")
	f.s.ProjectAccess = service
	authorizationExec(t, f.pool, "ALTER TABLE project_access_events RENAME TO missing_access_events")
	f.call(t, "owner", "GET", base, "", 503, "DEPENDENCY_UNAVAILABLE")
}

func TestOrganizationProjectHTTPStrictInputAndRevokedSession(t *testing.T) {
	f := projectHTTPDB(t)
	id := f.create(t, "validation")
	path := f.base() + "/" + id
	for _, input := range []string{`{}`, `{"slug":"valid","name":"X","owner_id":"other"}`, `{"slug":"valid","name":"X","visibility":"organization"}`, `{"slug":"valid","name":"X","name":"Y"}`, `{"slug":"valid","name":"X"} {}`, `{"slug":"valid","name":"\u0000"}`} {
		f.call(t, "owner", "PUT", f.base()+"/"+uuid.NewString(), input, 400, "INVALID_REQUEST")
	}
	for _, input := range []string{`{"role":"viewer","status":"active"}`, `{"role":"viewer","status":"active","revision":null}`, `{"role":"viewer","status":"active","revision":-1}`, `{"role":"viewer","status":"active","revision":1.5}`, `{"role":"owner","status":"active","revision":0}`} {
		f.call(t, "owner", "PUT", path+"/members/"+f.users["other"].ID, input, 400, "INVALID_REQUEST")
	}
	f.call(t, "owner", "PATCH", path+"/visibility", `{"visibility":"private"}`, 400, "INVALID_REQUEST")
	f.call(t, "owner", "PUT", path, strings.Repeat("x", 65537), 413, "PAYLOAD_TOO_LARGE")
	for _, suffix := range []string{"?limit=0", "?limit=101", "?limit=1&limit=2", "?cursor=", "?filter=private", "?limit=%zz"} {
		f.call(t, "owner", "GET", f.base()+suffix, "", 400, map[bool]string{true: "INVALID_CURSOR", false: "INVALID_REQUEST"}[suffix == "?cursor="])
	}
	f.call(t, "owner", "GET", "organizations/not-uuid/projects", "", 400, "INVALID_REQUEST")
	f.call(t, "owner", "GET", f.base()+"/"+uuid.Nil.String(), "", 400, "INVALID_REQUEST")
	f.call(t, "owner", "GET", path+"?cursor=anything", "", 400, "INVALID_REQUEST")
	f.call(t, "owner", "GET", path+"/access/candidates", "", 200, "")
	authorizationExec(t, f.pool, "DELETE FROM sessions WHERE user_id=$1", f.users["owner"].ID)
	f.call(t, "owner", "PUT", path, `{"slug":"validation","name":"Project"}`, 401, "UNAUTHORIZED")
}
