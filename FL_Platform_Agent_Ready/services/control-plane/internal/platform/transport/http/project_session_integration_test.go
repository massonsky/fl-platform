//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

func projectFixture(t *testing.T, acl bool) authorizationFixture {
	t.Helper()
	f := authorizationDB(t, false)
	if acl {
		for _, file := range []string{"000007_organizations.up.sql", "000008_project_access.up.sql"} {
			raw, err := os.ReadFile("/src/db/migrations/" + file)
			if err != nil {
				t.Fatal(err)
			}
			authorizationExec(t, f.pool, string(raw))
		}
	}
	for key, u := range f.users {
		u.SessionHash = identity.Hash(u.ID)
		f.users[key] = u
	}
	f.s.Experiments = experiment.Store{Pool: f.pool}
	return f
}

func projectFailure(t *testing.T, err error, status int, code string) {
	t.Helper()
	var f experiment.Failure
	if !errors.As(err, &f) || f.Status != status || f.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}

func projectHTTP(t *testing.T, f authorizationFixture, user, method, path, body string, status int, code string) []byte {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.users[user].ID)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "project-test-"+identity.Hash(path))
	w := httptest.NewRecorder()
	f.s.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s got%d want%d: %s", method, path, w.Code, status, w.Body.String())
	}
	if code != "" {
		var result struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Code != code {
			t.Fatalf("want%s: %s", code, w.Body.String())
		}
	}
	return w.Body.Bytes()
}

func projectResources(t *testing.T, f authorizationFixture, project, owner string) (string, string) {
	t.Helper()
	e, r := uuid.NewString(), uuid.NewString()
	authorizationExec(t, f.pool, `INSERT INTO experiments(id,project_id,name,config_json,config_hash) VALUES($1,$2,'Fixture','{"rounds":1}',$3)`, e, project, strings.Repeat("a", 64))
	authorizationExec(t, f.pool, `INSERT INTO runs(id,experiment_id,created_by,status,manifest_json,manifest_hash) VALUES($1,$2,$3,'queued','{"rounds":1}',$4)`, r, e, owner, strings.Repeat("a", 64))
	return e, r
}

func TestProjectSessionLegacyAndStaleIdentity(t *testing.T) {
	f := legacyWorkFixture(t)
	ctx := context.Background()
	user := f.users["owner"]
	access := experiment.Access{Principal: user.Principal()}
	raw := projectHTTP(t, f, "owner", "POST", "projects", `{"slug":"session-project","name":"Session project"}`, 201, "")
	var p struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ID == "" {
		t.Fatal(string(raw))
	}
	e, r := projectResources(t, f, p.ID, user.ID)
	projectHTTP(t, f, "owner", "GET", "runs/"+r, "", 200, "")
	projectHTTP(t, f, "owner", "GET", "projects/"+p.ID+"/runs", "", 200, "")
	projectHTTP(t, f, "other", "GET", "runs/"+r, "", 404, "NOT_FOUND")
	me := projectHTTP(t, f, "owner", "GET", "me", "", 200, "")
	if strings.Contains(string(me), user.SessionHash) || strings.Contains(string(me), "SessionHash") {
		t.Fatal("principal leaked through /me")
	}
	first, err := f.s.Experiments.Mutate(ctx, access, "queue", e, "session-replay-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.Experiments.Mutate(ctx, access, "queue", e, "session-replay-key", nil)
	var original, replay map[string]any
	if err != nil || json.Unmarshal(first.Body, &original) != nil || json.Unmarshal(second.Body, &replay) != nil || !reflect.DeepEqual(original, replay) {
		t.Fatal("legacy replay", err)
	}
	authorizationExec(t, f.pool, "UPDATE users SET role='viewer' WHERE id=$1", user.ID)
	_, err = f.s.Experiments.Mutate(ctx, access, "queue", e, "session-replay-key", nil)
	projectFailure(t, err, 403, "FORBIDDEN")
	if _, err = f.s.Experiments.Detail(ctx, access, "runs", r); err != nil {
		t.Fatal("viewer lost own read", err)
	}
	authorizationExec(t, f.pool, "UPDATE users SET disabled_at=now() WHERE id=$1", user.ID)
	_, err = f.s.Experiments.Detail(ctx, access, "runs", r)
	projectFailure(t, err, 401, "UNAUTHORIZED")
	authorizationExec(t, f.pool, "UPDATE users SET disabled_at=NULL,role='researcher' WHERE id=$1", user.ID)
	for _, p := range []identity.Principal{{UserID: user.ID}, {UserID: user.ID, SessionHash: f.users["other"].SessionHash}} {
		_, err = f.s.Experiments.Detail(ctx, experiment.Access{Principal: p}, "runs", r)
		projectFailure(t, err, 401, "UNAUTHORIZED")
	}
	if err = f.s.Identity.Logout(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Experiments.Mutate(ctx, access, "queue", e, "session-replay-key", nil)
	projectFailure(t, err, 401, "UNAUTHORIZED")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM http_receipts WHERE operation='queue'") != "1" {
		t.Fatal("denied replay created receipt")
	}
	// Password reset removes the old token even when the caller retained Principal.
	other := f.users["other"]
	authorizationHTTP(t, f.s, other, "account/password", `{"current_password":"valid-old-password","password":"valid-new-password"}`, 200, "")
	_, err = f.s.Experiments.List(ctx, experiment.Access{Principal: other.Principal()}, "projects", "", "", time.Now(), 0, 10)
	projectFailure(t, err, 401, "UNAUTHORIZED")
}

func TestProjectSessionSharedCannotUseLegacyRoutes(t *testing.T) {
	f := projectFixture(t, true)
	ctx := context.Background()
	owner := f.users["owner"]
	org, foreign := f.shared(t), f.shared(t)
	store := projectaccess.Store{Pool: f.pool}
	id := uuid.NewString()
	if _, err := store.Create(ctx, owner.ID, org, id, projectaccess.CreateInput{Slug: "shared-fixture", Name: "Shared fixture"}); err != nil {
		t.Fatal(err)
	}
	e, r := projectResources(t, f, id, owner.ID)
	for _, path := range []string{"projects/" + id + "/experiments", "projects/" + id + "/runs", "experiments/" + e, "runs/" + r, "runs/" + r + "/rounds", "runs/" + r + "/metrics", "runs/" + r + "/events", "runs/" + r + "/export"} {
		projectHTTP(t, f, "owner", "GET", path, "", 404, "NOT_FOUND")
	}
	for _, path := range []string{"projects/" + id + "/experiments", "experiments/" + e + "/runs", "runs/" + r + "/cancel"} {
		body := ""
		if strings.HasSuffix(path, "experiments") {
			body = "{}"
		}
		projectHTTP(t, f, "owner", "POST", path, body, 404, "NOT_FOUND")
	}
	if strings.Contains(string(projectHTTP(t, f, "owner", "GET", "projects", "", 200, "")), id) {
		t.Fatal("legacy list exposed shared project")
	}
	access := experiment.Access{Principal: owner.Principal(), OrganizationID: org}
	if _, err := f.s.Experiments.Detail(ctx, access, "runs", r); err != nil {
		t.Fatal("explicit own org read", err)
	}
	wrong := access
	wrong.OrganizationID = foreign
	_, err := f.s.Experiments.Detail(ctx, wrong, "runs", r)
	projectFailure(t, err, 404, "NOT_FOUND")
	_, err = f.s.Experiments.Mutate(ctx, access, "queue", e, "shared-queue-key", nil)
	projectFailure(t, err, 503, "ORG_AGENTS_NOT_READY")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM runs") != "1" || authorizationValue(t, f.pool, "SELECT count(*)::text FROM http_receipts") != "0" {
		t.Fatal("shared queue created work/receipt")
	}
	// Creator loses inherited admin access, then an explicit viewer caps writes.
	other := f.users["other"].ID
	authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", org, other)
	authorizationExec(t, f.pool, "UPDATE organization_memberships SET role='researcher' WHERE organization_id=$1 AND user_id=$2", org, owner.ID)
	if _, err = store.SaveGrant(ctx, other, org, id, owner.ID, projectaccess.GrantInput{Role: "viewer", Status: "active", Revision: 0}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Experiments.Mutate(ctx, access, "cancel", r, "shared-cancel-key", nil)
	projectFailure(t, err, 403, "FORBIDDEN")
	if _, err = store.SaveGrant(ctx, other, org, id, owner.ID, projectaccess.GrantInput{Role: "researcher", Status: "active", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Experiments.Mutate(ctx, access, "cancel", r, "shared-cancel-key", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveGrant(ctx, other, org, id, owner.ID, projectaccess.GrantInput{Role: "researcher", Status: "revoked", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Experiments.Mutate(ctx, access, "cancel", r, "shared-cancel-key", nil)
	projectFailure(t, err, 404, "NOT_FOUND")
	_, err = f.s.Experiments.Detail(ctx, access, "runs", r)
	projectFailure(t, err, 404, "NOT_FOUND")
}
