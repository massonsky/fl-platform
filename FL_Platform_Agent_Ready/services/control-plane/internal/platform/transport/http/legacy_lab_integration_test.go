//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/legacylab"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func legacyLabHTTPDB(t *testing.T, version int) authorizationFixture {
	t.Helper()
	f := authorizationDB(t, false)
	for i, file := range []string{"000007_organizations.up.sql", "000008_project_access.up.sql", "000009_legacy_lab_admission.up.sql"} {
		if i+7 > version {
			break
		}
		raw, err := os.ReadFile("/src/db/migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		authorizationExec(t, f.pool, string(raw))
	}
	return f
}
func legacyLabRequest(f authorizationFixture, token, method, path, input string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(input))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	if method == "POST" {
		r.Header.Set("Idempotency-Key", "legacy-lab-http-fixture")
	}
	w := httptest.NewRecorder()
	f.s.ServeHTTP(w, r)
	return w
}
func legacyLabResponse(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("response status%d, want%d", w.Code, status)
	}
	if code != "" {
		var e struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		}
		if json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Code != code || e.Retryable != (status >= 500) || w.Header().Get("X-Request-ID") == "" {
			t.Fatal("unexpected safe error envelope")
		}
	}
	if status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing challenge")
	}
	if status == 503 && (w.Header().Get("Retry-After") != "3" || w.Header().Get("WWW-Authenticate") != "") {
		t.Fatal("unsafe dependency headers")
	}
}
func legacyLabLogin(t *testing.T, f authorizationFixture, username, password string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"username": username, "password": password})
	w := legacyLabRequest(f, "", "POST", "/api/v1/session", string(raw))
	legacyLabResponse(t, w, 200, "")
	var result struct{ Token string }
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token == "" {
		t.Fatal("login did not produce session")
	}
	return result.Token
}
func legacyLabDTO(t *testing.T, w *httptest.ResponseRecorder) legacylab.Admission {
	t.Helper()
	legacyLabResponse(t, w, 200, "")
	var a legacylab.Admission
	if json.Unmarshal(w.Body.Bytes(), &a) != nil || a.UserID == "" || strings.Contains(w.Body.String(), "SessionHash") {
		t.Fatal("invalid/private admission DTO")
	}
	return a
}

func TestLegacyLabHTTPLoginGrantReadAndCAS(t *testing.T) {
	f := legacyLabHTTPDB(t, 9)
	configureCredentialLogin(t, f.s)
	admin := legacyLabLogin(t, f, "admin", "valid-old-password")
	created := legacyLabRequest(f, admin, "POST", "/api/v1/admin/users", `{"username":"external-lab-user","name":"External user","role":"researcher","password":"valid-new-password"}`)
	legacyLabResponse(t, created, 201, "")
	var user struct{ ID string }
	if json.Unmarshal(created.Body.Bytes(), &user) != nil || user.ID == "" {
		t.Fatal("missing new identity")
	}
	token := legacyLabLogin(t, f, "external-lab-user", "valid-new-password")
	path := "/api/v1/admin/users/" + user.ID + "/legacy-lab"
	self := "/api/v1/me/legacy-lab"
	a := legacyLabDTO(t, legacyLabRequest(f, token, "GET", self, ""))
	if a.UserID != user.ID || a.Status != "not_admitted" || a.Revision != 0 || a.Source != "" || a.UpdatedAt != nil || a.CanCreateLegacyWork {
		t.Fatal("new external identity inherited admission")
	}
	for _, actor := range []string{token, f.users["viewer"].ID, f.users["owner"].ID} {
		legacyLabResponse(t, legacyLabRequest(f, actor, "GET", path, ""), 403, "FORBIDDEN")
		legacyLabResponse(t, legacyLabRequest(f, actor, "PUT", path, `{"status":"active","revision":0,"reason":"forged"}`), 403, "FORBIDDEN")
	}
	a = legacyLabDTO(t, legacyLabRequest(f, admin, "PUT", path, `{"status":"active","revision":0,"reason":"  Operator reviewed  "}`))
	if a.Revision != 1 || a.Source != "admin" || !a.CanCreateLegacyWork || a.UpdatedAt == nil {
		t.Fatal("explicit grant not reflected")
	}
	before := authorizationValue(t, f.pool, "SELECT jsonb_agg(to_jsonb(e) ORDER BY id)::text FROM legacy_lab_admission_events e")
	legacyLabDTO(t, legacyLabRequest(f, admin, "PUT", path, `{"status":"active","revision":1,"reason":"No-op"}`))
	legacyLabResponse(t, legacyLabRequest(f, admin, "PUT", path, `{"status":"active","revision":0,"reason":"Retry"}`), 409, "VERSION_CONFLICT")
	if authorizationValue(t, f.pool, "SELECT jsonb_agg(to_jsonb(e) ORDER BY id)::text FROM legacy_lab_admission_events e") != before || authorizationValue(t, f.pool, "SELECT reason FROM legacy_lab_admission_events WHERE target_user_id=$1", user.ID) != "Operator reviewed" {
		t.Fatal("no-op/conflict duplicated audit or reason not normalized")
	}
	if !legacyLabDTO(t, legacyLabRequest(f, token, "GET", self, "")).CanCreateLegacyWork {
		t.Fatal("self capability not refreshed")
	}
	a = legacyLabDTO(t, legacyLabRequest(f, admin, "PUT", path, `{"status":"revoked","revision":1,"reason":"Withdraw new work"}`))
	if a.Revision != 2 || a.CanCreateLegacyWork || a.Status != "revoked" {
		t.Fatal("revocation not reflected")
	}
	viewerPath := "/api/v1/admin/users/" + f.users["viewer"].ID + "/legacy-lab"
	if legacyLabDTO(t, legacyLabRequest(f, admin, "PUT", viewerPath, `{"status":"active","revision":0,"reason":"Metadata only"}`)).CanCreateLegacyWork ||
		legacyLabDTO(t, legacyLabRequest(f, admin, "GET", "/api/v1/admin/users/"+f.users["disabled"].ID+"/legacy-lab", "")).CanCreateLegacyWork {
		t.Fatal("global viewer/disabled cap bypassed")
	}
	legacyLabResponse(t, legacyLabRequest(f, admin, "GET", "/api/v1/admin/users/"+uuid.NewString()+"/legacy-lab", ""), 404, "NOT_FOUND")
	for _, bad := range []string{path + "/", path + "/extra", self + "?user_id=" + user.ID, "/me/legacy-lab"} {
		status, code := 404, "NOT_FOUND"
		if strings.Contains(bad, "?") {
			status, code = 400, "INVALID_REQUEST"
		}
		legacyLabResponse(t, legacyLabRequest(f, admin, "GET", bad, ""), status, code)
	}
}

func TestLegacyLabHTTPCurrentSessionAndConcurrentRevision(t *testing.T) {
	f := legacyLabHTTPDB(t, 9)
	path := "/api/v1/admin/users/" + f.users["owner"].ID + "/legacy-lab"
	start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			done <- legacyLabRequest(f, f.users["admin"].ID, "PUT", path, `{"status":"revoked","revision":1,"reason":"Concurrent revoke"}`)
		}()
	}
	close(start)
	first, second := <-done, <-done
	if first.Code > second.Code {
		first, second = second, first
	}
	legacyLabResponse(t, first, 200, "")
	legacyLabResponse(t, second, 409, "VERSION_CONFLICT")
	p := f.users["admin"].Principal()
	authorizationExec(t, f.pool, "UPDATE users SET role='viewer' WHERE id=$1", p.UserID)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", path, strings.NewReader(`{"status":"active","revision":2,"reason":"Stale role"}`))
	r.Header.Set("Content-Type", "application/json")
	f.s.legacyLab(w, r, p, strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/"))
	legacyLabResponse(t, w, 403, "FORBIDDEN")
	legacyLabResponse(t, legacyLabRequest(f, f.users["admin"].ID, "GET", path, ""), 403, "FORBIDDEN")
	authorizationExec(t, f.pool, "UPDATE users SET role='admin' WHERE id=$1", p.UserID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if err = authorization.Write(ctx, hold); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		f.s.legacyLab(w, httptest.NewRequest("GET", "/api/v1/me/legacy-lab", nil), p, []string{"me", "legacy-lab"})
		waiting <- w
	}()
	waitProjectGuard(t, ctx, f.pool, "ShareLock")
	if _, err = hold.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", p.SessionHash); err != nil {
		t.Fatal(err)
	}
	if err = hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	legacyLabResponse(t, <-waiting, 401, "UNAUTHORIZED")
}

func TestLegacyLabHTTPSchemaAndValidationFailuresDoNotWrite(t *testing.T) {
	for _, version := range []int{6, 8, 9} {
		f := legacyLabHTTPDB(t, version)
		if version == 9 {
			authorizationExec(t, f.pool, "ALTER TABLE legacy_lab_admission_events RENAME TO unavailable_lab_events")
		}
		path := "/api/v1/admin/users/" + f.users["owner"].ID + "/legacy-lab"
		for _, endpoint := range []string{"/api/v1/me/legacy-lab", path} {
			legacyLabResponse(t, legacyLabRequest(f, f.users["admin"].ID, "GET", endpoint, ""), 503, "DEPENDENCY_UNAVAILABLE")
		}
		legacyLabResponse(t, legacyLabRequest(f, f.users["admin"].ID, "PUT", path, `{"status":"active","revision":0,"reason":"Review"}`), 503, "DEPENDENCY_UNAVAILABLE")
	}
	f := legacyLabHTTPDB(t, 9)
	path := "/api/v1/admin/users/" + f.users["owner"].ID + "/legacy-lab"
	before := authorizationValue(t, f.pool, "SELECT jsonb_agg(to_jsonb(a) ORDER BY user_id)::text FROM legacy_lab_admissions a")
	for _, input := range []string{`{"status":"active","reason":"missing"}`, `{"status":"active","revision":2147483648,"reason":"overflow"}`, `{"status":"active","revision":1,"revision":2,"reason":"duplicate"}`} {
		legacyLabResponse(t, legacyLabRequest(f, f.users["admin"].ID, "PUT", path, input), 400, "INVALID_REQUEST")
	}
	legacyLabResponse(t, legacyLabRequest(f, "", "GET", "/api/v1/me/legacy-lab", ""), 401, "UNAUTHORIZED")
	if authorizationValue(t, f.pool, "SELECT jsonb_agg(to_jsonb(a) ORDER BY user_id)::text FROM legacy_lab_admissions a") != before {
		t.Fatal("invalid request changed admission")
	}
}
