//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func adminSessionFailure(t *testing.T, err error, status int, code string) {
	t.Helper()
	var f admin.Failure
	if !errors.As(err, &f) || f.Status != status || f.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}
func adminSessionPrincipal(t *testing.T, f authorizationFixture, actor string) identity.Principal {
	t.Helper()
	p := identity.Principal{UserID: actor, SessionHash: identity.Hash(uuid.NewString())}
	authorizationExec(t, f.pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')", p.SessionHash, actor)
	return p
}
func adminSessionSnapshot(t *testing.T, f authorizationFixture) string {
	t.Helper()
	return authorizationValue(t, f.pool, `SELECT jsonb_build_object(
 'users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),
 'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY token_hash) FROM sessions s),
 'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM administration_events e),
 'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY actor_id,operation,idempotency_key) FROM administration_receipts r))::text`)
}

func TestAdministrationServiceSessionBeforePageMutationAndReplay(t *testing.T) {
	f := authorizationDB(t, false)
	ctx := context.Background()
	s := configureCredentialAdministration(t, f.s)
	p := f.users["admin"].Principal()
	in := admin.Input{Username: "session-fixture", Name: "Session fixture", Role: "viewer", Password: "valid-fixture-password"}
	// The HTTP adapter supplies a 64-hex HMAC; char(64) pads shorter fixtures.
	fingerprint := identity.Hash("isolated-canonical-request")
	command := administrationCredentialCommand("user.create", "", "admin-session-key", fingerprint, in)
	first, err := s.MutateCredential(ctx, p, command)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.MutateCredential(ctx, p, command)
	var original, repeated struct {
		ID    string `json:"id"`
		Saved bool   `json:"saved"`
	}
	// JSONB may reformat whitespace; the persisted identity/result must match.
	if err != nil || json.Unmarshal(first, &original) != nil || json.Unmarshal(replay, &repeated) != nil || original != repeated || original.ID == "" || !original.Saved {
		t.Fatal("authorized replay changed", err)
	}
	if authorizationValue(t, f.pool, "SELECT count(*)::text FROM users WHERE external_subject='session-fixture'") != "1" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_receipts") != "1" ||
		authorizationValue(t, f.pool, "SELECT count(*)::text FROM administration_events") != "1" {
		t.Fatal("authorized replay duplicated user/receipt/audit")
	}
	beforeConflict := adminSessionSnapshot(t, f)
	conflicting := command
	conflicting.Fingerprint = identity.Hash("different-canonical-request")
	conflict, err := s.MutateCredential(ctx, p, conflicting)
	adminSessionFailure(t, err, 409, "IDEMPOTENCY_CONFLICT")
	if conflict != nil || adminSessionSnapshot(t, f) != beforeConflict {
		t.Fatal("conflicting replay returned DTO or changed state")
	}
	expired := adminSessionPrincipal(t, f, p.UserID)
	authorizationExec(t, f.pool, "UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1", expired.SessionHash)
	revoked := adminSessionPrincipal(t, f, p.UserID)
	authorizationExec(t, f.pool, "DELETE FROM sessions WHERE token_hash=$1", revoked.SessionHash)
	cases := map[string]identity.Principal{"missing": {}, "missinghash": {UserID: p.UserID}, "malformed": {UserID: "not-uuid", SessionHash: p.SessionHash},
		"foreign": {UserID: p.UserID, SessionHash: f.users["other"].SessionHash}, "expired": expired, "revoked": revoked, "disabled": f.users["disabled"].Principal()}
	for name, principal := range cases {
		t.Run(name, func(t *testing.T) {
			before := adminSessionSnapshot(t, f)
			for _, kind := range []string{"users", "agents", "events"} {
				items, more, err := s.Page(ctx, principal, kind, 0, 10)
				adminSessionFailure(t, err, 401, "UNAUTHORIZED")
				if items != nil || more {
					t.Fatal("unauthorized page returned rows")
				}
			}
			got, err := s.MutateCredential(ctx, principal, command)
			adminSessionFailure(t, err, 401, "UNAUTHORIZED")
			if got != nil {
				t.Fatal("unauthorized replay returned DTO")
			}
			if adminSessionSnapshot(t, f) != before {
				t.Fatal("unauthorized operation changed users/sessions/audit/receipts")
			}
		})
	}
	// A stale admin role cannot replay after a current role demotion.
	authorizationExec(t, f.pool, "UPDATE users SET role='viewer' WHERE id=$1", p.UserID)
	_, _, err = s.Page(ctx, p, "users", 0, 10)
	adminSessionFailure(t, err, 403, "FORBIDDEN")
	_, err = s.MutateCredential(ctx, p, command)
	adminSessionFailure(t, err, 403, "FORBIDDEN")
	authorizationHTTP(t, f.s, f.users["admin"], "admin/users", "", 403, "FORBIDDEN")
}

func TestAdministrationHTTPUsesCurrentPrincipalAndPasswordBoundary(t *testing.T) {
	f := authorizationDB(t, false)
	configureCredentialAdministration(t, f.s)
	// Early authentication observed a viewer; current enabled admin may read.
	viewer := f.users["viewer"]
	authorizationExec(t, f.pool, "UPDATE users SET role='admin' WHERE id=$1", viewer.ID)
	authorizationHTTP(t, f.s, viewer, "admin/users", "", 200, "")
	owner := f.users["owner"]
	before := authorizationValue(t, f.pool, "SELECT password_hash FROM users WHERE id=$1", owner.ID)
	authorizationHTTP(t, f.s, owner, "account/password", `{"current_password":"valid-old-password","password":"valid-new-password"}`, 200, "")
	if authorizationValue(t, f.pool, "SELECT password_hash FROM users WHERE id=$1", owner.ID) == before || authorizationValue(t, f.pool, "SELECT count(*)::text FROM sessions WHERE user_id=$1", owner.ID) != "0" {
		t.Fatal("password change did not commit and revoke")
	}
	authorizationHTTP(t, f.s, owner, "account/password", `{"current_password":"valid-old-password","password":"valid-new-password"}`, 401, "UNAUTHORIZED")
	if err := f.s.Identity.Logout(context.Background(), f.users["admin"].ID); err != nil {
		t.Fatal(err)
	}
	authorizationHTTP(t, f.s, f.users["admin"], "admin/users", "", 401, "UNAUTHORIZED")
	authorizationHTTP(t, f.s, f.users["admin"], "admin/users/"+viewer.ID, `{"role":"viewer","disabled":false}`, 401, "UNAUTHORIZED")
	// Login creates a new session, and the same identity can replay only with it.
	token, err := f.s.Identity.Login(context.Background(), identity.LoginInput{Client: netip.MustParseAddr("192.0.2.1"), Username: "owner", Password: "valid-new-password"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.s.Identity.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	authorizationHTTP(t, f.s, current, "account/password", `{"current_password":"valid-old-password","password":"valid-new-password"}`, 200, "")
	if strings.Contains(string(authorizationHTTP(t, f.s, viewer, "admin/users", "", 200, "")), "SessionHash") {
		t.Fatal("hidden principal serialized")
	}
}
