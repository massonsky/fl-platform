//go:build integration

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
)

type authorizationFixture struct {
	pool  *pgxpool.Pool
	s     *Server
	users map[string]identity.User
}

func authorizationDB(t *testing.T, organizations bool) authorizationFixture {
	t.Helper()
	f := authorizationFixture{pool: aiDatabase(t), users: map[string]identity.User{}}
	if organizations {
		raw, err := os.ReadFile("/src/db/migrations/000007_organizations.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		authorizationExec(t, f.pool, string(raw))
	}
	f.s = &Server{Identity: identity.Store{Pool: f.pool}, CursorKey: []byte("isolated-authorization-test")}
	for name, role := range map[string]string{"admin": "admin", "owner": "researcher", "other": "researcher", "viewer": "viewer", "disabled": "researcher"} {
		id := uuid.NewString()
		f.users[name] = identity.User{ID: id, Role: role, SessionHash: identity.Hash(id)}
		authorizationExec(t, f.pool, `INSERT INTO users(id,external_subject,display_name,role,password_hash)
 VALUES($1,$2,$2,$3,$4)`, id, name, role, identity.PasswordHash("valid-old-password"))
		authorizationExec(t, f.pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')", identity.Hash(id), id)
	}
	authorizationExec(t, f.pool, "UPDATE users SET disabled_at=now() WHERE id=$1", f.users["disabled"].ID)
	return f
}

func authorizationExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func authorizationValue(t *testing.T, pool *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// Supply the identity observed at authentication time, so the adapter must
// independently reject stale roles and distinguish dependency errors.
func authorizationHTTP(t *testing.T, s *Server, actor identity.User, path, input string, status int, code string) []byte {
	t.Helper()
	method := "GET"
	if input != "" {
		method = "POST"
	}
	r := httptest.NewRequest(method, "/api/v1/"+path, strings.NewReader(input))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "identity-change-"+identity.Hash(path))
	w := httptest.NewRecorder()
	s.administration(w, r, actor, path)
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	if code != "" {
		var body struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Code != code {
			t.Fatalf("want %s: %s", code, w.Body.String())
		}
		if status == 503 && !body.Retryable {
			t.Fatal("dependency error must allow retry")
		}
	}
	if status == 503 && (w.Header().Get("WWW-Authenticate") != "" || w.Header().Get("Retry-After") != "3") {
		t.Fatal("database outage must allow retry after 3 seconds without challenging credentials")
	}
	return w.Body.Bytes()
}

func (f authorizationFixture) shared(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := (organization.Store{Pool: f.pool}).Create(context.Background(), f.users["owner"].ID, id, "Authorization fixture"); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAuthorizationLegacySchemaSix(t *testing.T) {
	f := authorizationDB(t, false)
	admin, owner, other := f.users["admin"], f.users["owner"], f.users["other"]
	body := `{"role":"viewer","disabled":false}`
	path := "admin/users/" + owner.ID
	first := authorizationHTTP(t, f.s, admin, path, body, 200, "")
	if !bytes.Equal(first, authorizationHTTP(t, f.s, admin, path, body, 200, "")) {
		t.Fatal("durable replay changed response")
	}
	authorizationHTTP(t, f.s, admin, path, `{"role":"researcher","disabled":false}`, 409, "IDEMPOTENCY_CONFLICT")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM sessions WHERE user_id=$1", owner.ID) != "0" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_events") != "1" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_receipts") != "1" {
		t.Fatal("legacy update/session/receipt/audit regression")
	}
	authorizationHTTP(t, f.s, admin, "admin/users/"+admin.ID, body, 409, "LAST_ADMIN")
	before := adminSessionSnapshot(t, f)
	authorizationHTTP(t, f.s, other, "account/password", `{"current_password":"valid-old-password","password":"valid-new-password"}`, 503, "DEPENDENCY_UNAVAILABLE")
	if adminSessionSnapshot(t, f) != before {
		t.Fatal("schema6 credential rejection changed password/session/audit/receipt")
	}
}

func TestAuthorizationCredentialPasswordSessionRevocation(t *testing.T) {
	f := authorizationDB(t, false)
	configureCredentialAdministration(t, f.s)
	other := f.users["other"]
	before := authorizationValue(t, f.pool, "SELECT password_hash FROM users WHERE id=$1", other.ID)
	authorizationHTTP(t, f.s, other, "account/password", `{"current_password":"valid-old-password","password":"valid-new-password"}`, 200, "")
	after := authorizationValue(t, f.pool, "SELECT password_hash FROM users WHERE id=$1", other.ID)
	if after == before || !identity.Verify("valid-new-password", after) ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM sessions WHERE user_id=$1", other.ID) != "0" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_events") != "1" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_receipts") != "1" {
		t.Fatal("password change did not commit/revoke/audit/receipt exactly once")
	}
}

func TestAuthorizationActiveSharedOwnerRollback(t *testing.T) {
	f := authorizationDB(t, true)
	configureCredentialAdministration(t, f.s)
	id, owner := f.shared(t), f.users["owner"]
	for _, name := range []string{"viewer", "disabled"} {
		authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", id, f.users[name].ID)
	}
	before := authorizationValue(t, f.pool, "SELECT password_hash FROM users WHERE id=$1", owner.ID)
	for _, body := range []string{`{"role":"viewer","disabled":false}`, `{"role":"researcher","disabled":true,"password":"valid-new-password"}`} {
		authorizationHTTP(t, f.s, f.users["admin"], "admin/users/"+owner.ID, body, 409, "LAST_OWNER")
	}
	if authorizationValue(t, f.pool, "SELECT role||':'||(disabled_at IS NOT NULL)::text FROM users WHERE id=$1", owner.ID) != "researcher:false" ||
		authorizationValue(t, f.pool, "SELECT password_hash FROM users WHERE id=$1", owner.ID) != before ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM sessions WHERE user_id=$1", owner.ID) != "1" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_events") != "0" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_receipts") != "0" {
		t.Fatal("denied owner change was not fully rolled back")
	}
	authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", id, f.users["other"].ID)
	authorizationHTTP(t, f.s, f.users["admin"], "admin/users/"+owner.ID, `{"role":"researcher","disabled":true}`, 200, "")
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM sessions WHERE user_id=$1", owner.ID) != "0" {
		t.Fatal("accepted owner change retained sessions")
	}
}

func TestAuthorizationPersonalAndSuspendedCompatibility(t *testing.T) {
	for _, kind := range []string{"personal", "suspended"} {
		t.Run(kind, func(t *testing.T) {
			f := authorizationDB(t, true)
			id, owner := f.shared(t), f.users["owner"]
			if kind == "personal" {
				authorizationExec(t, f.pool, "UPDATE organizations SET kind='personal',personal_owner_id=$2 WHERE id=$1", id, owner.ID)
			} else {
				authorizationExec(t, f.pool, "UPDATE organizations SET state='suspended' WHERE id=$1", id)
			}
			authorizationHTTP(t, f.s, f.users["admin"], "admin/users/"+owner.ID, `{"role":"viewer","disabled":true}`, 200, "")
			if authorizationValue(t, f.pool, "SELECT count(*)::text FROM organization_memberships WHERE organization_id=$1 AND user_id=$2", id, owner.ID) != "1" ||
				authorizationValue(t, f.pool, "SELECT count(*)::text FROM organizations WHERE id=$1", id) != "1" {
				t.Fatal("identity change removed organization metadata")
			}
		})
	}
}
