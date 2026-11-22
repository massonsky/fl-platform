//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func administrationCredentialRequest(s *Server, actor identity.User, method, path, key, raw, peer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1/"+path, strings.NewReader(raw))
	r.RemoteAddr = peer
	r.Header.Set("Authorization", "Bearer "+actor.ID)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAdministrationCredentialHTTPSharedWorkAndBudgets(t *testing.T) {
	f := authorizationDB(t, false)
	configureCredentialAdministration(t, f.s)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		finished <- f.s.Identity.Work.Do(context.Background(), func(context.Context) error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("shared work fixture ended before pause", err)
	case <-time.After(time.Second):
		t.Fatal("shared work fixture did not pause")
	}
	body := `{"username":"credential-created","name":"Created","role":"viewer","password":"valid-new-password"}`
	request := func(actor identity.User, key, raw, peer string) *httptest.ResponseRecorder {
		return administrationCredentialRequest(f.s, actor, "POST", "admin/users", key, raw, peer)
	}
	checkCredentialError(t, request(f.users["admin"], "busy-admin-key", body, "192.0.2.1:1000"), 503, "DEPENDENCY_UNAVAILABLE")
	checkCredentialError(t, credentialRequest(f.s, credentialBody("admin", "valid-old-password"), "192.0.2.1:1001"), 503, "DEPENDENCY_UNAVAILABLE")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM credential_limit_buckets WHERE kind IN ('actor','account')") != "0" {
		t.Fatal("busy shared permit admitted credential work")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if w := request(f.users["admin"], "created-admin-key", body, "192.0.2.1:1000"); w.Code != 201 {
		t.Fatalf("create after release: %d", w.Code)
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM credential_limit_buckets WHERE kind='actor'") != "1" {
		t.Fatal("HTTP skipped actor budget")
	}
	// Set this isolated budget to its full burst without a timing race from real
	// Argon/refill. Shared multi-replica natural debit is covered by foundation.
	authorizationExec(t, f.pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()+interval '18 seconds',expires_at=clock_timestamp()+interval '1 hour' WHERE kind='actor'")
	before := adminSessionSnapshot(t, f)
	w := request(f.users["admin"], "rate-admin-key", strings.Replace(body, "credential-created", "rate-created", 1), "192.0.2.2:1000")
	checkCredentialError(t, w, 429, "RATE_LIMITED")
	retry, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || retry < 1 || retry > 6 {
		t.Fatal("missing dynamic actor retry")
	}
	if adminSessionSnapshot(t, f) != before {
		t.Fatal("rate denial wrote user/audit/receipt")
	}
	authorizationExec(t, f.pool, "UPDATE users SET role='admin' WHERE id=$1", f.users["other"].ID)
	w = request(f.users["other"], "other-admin-key", strings.Replace(body, "credential-created", "other-created", 1), "192.0.2.3:1000")
	if w.Code != 201 {
		t.Fatalf("independent current admin denied: %d", w.Code)
	}
	authorizationExec(t, f.pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()+interval '1 hour',expires_at=clock_timestamp()+interval '2 hours' WHERE kind='client'")
	checkCredentialError(t, request(f.users["other"], "spoof-admin-key", body, "192.0.2.3:1000"), 429, "RATE_LIMITED")
}

func TestAdministrationCredentialHTTPMissingDependenciesAndReplay(t *testing.T) {
	f := authorizationDB(t, false)
	configureCredentialAdministration(t, f.s)
	actor := f.users["admin"]
	raw := `{"username":"replay-created","name":"Replay","role":"viewer","password":"valid-new-password"}`
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		return administrationCredentialRequest(f.s, actor, method, path, key, body, "192.0.2.1:1000")
	}
	w := call("POST", "admin/users", "replay-admin-key", raw)
	var saved struct {
		ID    string
		Saved bool
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.ID == "" || !saved.Saved {
		t.Fatal("missing created user")
	}
	f.s.Identity.Limits = nil
	f.s.Identity.Work = nil
	before := adminSessionSnapshot(t, f)
	w = call("POST", "admin/users", "replay-admin-key", raw)
	var again struct {
		ID    string
		Saved bool
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &again) != nil || again != saved {
		t.Fatal("authorized replay required new work")
	}
	checkCredentialError(t, call("POST", "admin/users", "new-admin-key", raw), 503, "DEPENDENCY_UNAVAILABLE")
	checkCredentialError(t, call("POST", "admin/users/"+saved.ID, "reset-admin-key", `{"role":"viewer","disabled":false,"password":"another-valid-password"}`), 503, "DEPENDENCY_UNAVAILABLE")
	if adminSessionSnapshot(t, f) != before {
		t.Fatal("missing dependencies changed business state")
	}
	if w = call("GET", "admin/users", "", ""); w.Code != 200 {
		t.Fatal("missing key disabled read")
	}
	if w = call("POST", "admin/users/"+saved.ID, "role-only-key", `{"role":"researcher","disabled":false}`); w.Code != 200 {
		t.Fatal("missing key disabled noncredential update")
	}
	if w = call("GET", "me", "", ""); w.Code != 200 {
		t.Fatal("missing key disabled existing session")
	}
	if w = call("DELETE", "session", "", ""); w.Code != 200 {
		t.Fatal("missing key disabled logout")
	}
}

func TestAdministrationCredentialHTTPRejectsLegacyPathsAndDependencyDrift(t *testing.T) {
	f := authorizationDB(t, false)
	service := configureCredentialAdministration(t, f.s)
	actor := f.users["admin"]
	input := admin.Input{Username: "legacy-denied", Name: "Denied", Role: "viewer", Password: "valid-new-password"}
	before := adminSessionSnapshot(t, f)
	out, err := service.Mutate(context.Background(), actor.Principal(), "user.create", "", "old-service-key", identity.Hash("old-service"), input)
	adminSessionFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if out != nil {
		t.Fatal("legacy service returned DTO")
	}
	out, err = (admin.Store{Pool: f.pool}).Mutate(context.Background(), actor.ID, "user.create", "", "old-store-key", identity.Hash("old-store"), input)
	adminSessionFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if out != nil || adminSessionSnapshot(t, f) != before {
		t.Fatal("actor-only path bypassed prepared work")
	}
	for _, raw := range []string{
		`{"username":"bad-input","name":"Bad","role":"viewer","password":"short"}`,
		`{"username":"bad-input","name":"Bad","role":"viewer","password":"valid-new-password","client":"192.0.2.50"}`,
	} {
		checkCredentialError(t, administrationCredentialRequest(f.s, actor, "POST", "admin/users", "invalid-admin-key", raw, "192.0.2.1:1000"), 400, "INVALID_REQUEST")
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM credential_limit_buckets") != "0" {
		t.Fatal("invalid DTO consumed work budget")
	}
	checkCredentialError(t, administrationCredentialRequest(f.s, f.users["owner"], "POST", "account/password", "wrong-current-key", `{"current_password":"wrong-password","password":"valid-new-password"}`, "192.0.2.1:1000"), 403, "CURRENT_PASSWORD_INVALID")
	authorizationExec(t, f.pool, "ALTER TABLE credential_limit_buckets RENAME TO unavailable_credential_buckets")
	checkCredentialError(t, administrationCredentialRequest(f.s, actor, "POST", "admin/users", "schema-admin-key", `{"username":"schema-denied","name":"Denied","role":"viewer","password":"valid-new-password"}`, "192.0.2.1:1000"), 503, "DEPENDENCY_UNAVAILABLE")
	if adminSessionSnapshot(t, f) != before {
		t.Fatal("credential denial changed business state")
	}
	if w := administrationCredentialRequest(f.s, actor, "GET", "admin/users", "", "", "192.0.2.1:1000"); w.Code != 200 {
		t.Fatal("schema outage disabled read")
	}
}
