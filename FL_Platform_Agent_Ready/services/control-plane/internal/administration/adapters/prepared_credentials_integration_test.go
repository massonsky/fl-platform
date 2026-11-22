//go:build integration

package adapters

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"

	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func TestPreparedCredentialsRealCreateResetSelf(t *testing.T) {
	for _, op := range []string{"user.create", "user.update", "password.change"} {
		t.Run(op, func(t *testing.T) {
			f := preparedFixture(t)
			command := f.command(op)
			actor := f.users["admin"]
			if op == "password.change" {
				actor = f.users["owner"]
			}
			output, err := f.s.MutateCredential(context.Background(), actor, command)
			var saved struct {
				ID    string
				Saved bool
			}
			if err != nil || json.Unmarshal(output, &saved) != nil || saved.ID == "" || !saved.Saved {
				t.Fatal("credential success missing", err)
			}
			hash := credentialValue(t, f.s.Pool, "SELECT password_hash FROM users WHERE id=$1", saved.ID)
			if !identity.Verify(command.Input.Password, hash) {
				t.Fatal("new password did not verify")
			}
			if op != "user.create" && credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM sessions WHERE user_id=$1", saved.ID) != "0" {
				t.Fatal("target sessions retained")
			}
			if credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM administration_events") != "1" || credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM administration_receipts") != "1" {
				t.Fatal("effect was not atomic")
			}
			if op == "password.change" {
				out, err := f.s.MutateCredential(context.Background(), actor, command)
				credentialFailure(t, out, err, 401, "UNAUTHORIZED")
				actor.SessionHash = identity.Hash("fresh-fixture-session")
				credentialExec(t, f.s.Pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')", actor.SessionHash, actor.UserID)
			}
			before := credentialState(t, f.s.Pool)
			budget := credentialValue(t, f.s.Pool, "SELECT jsonb_agg(to_jsonb(b) ORDER BY kind,digest)::text FROM credential_limit_buckets b")
			replayService := Service{Pool: f.s.Pool}
			replay, err := replayService.MutateCredential(context.Background(), actor, command)
			var repeated struct {
				ID    string
				Saved bool
			}
			if err != nil || json.Unmarshal(replay, &repeated) != nil || repeated != saved {
				t.Fatal("authorized persisted replay changed", err)
			}
			if credentialState(t, f.s.Pool) != before || credentialValue(t, f.s.Pool, "SELECT jsonb_agg(to_jsonb(b) ORDER BY kind,digest)::text FROM credential_limit_buckets b") != budget {
				t.Fatal("replay mutated state or charged budget")
			}
		})
	}
}

func TestPreparedWrongCurrentReservesTwoAndSkipsHash(t *testing.T) {
	f := preparedFixture(t)
	command := f.command("password.change")
	command.Input.CurrentPassword = "wrong-password"
	hashes := 0
	f.s.passwordHash = func(string) string { hashes++; return "not-used" }
	f.s.verifyPassword = func(string, string) bool { return false }
	before := credentialState(t, f.s.Pool)
	output, err := f.s.MutateCredential(context.Background(), f.users["owner"], command)
	credentialFailure(t, output, err, 403, "CURRENT_PASSWORD_INVALID")
	if hashes != 0 || credentialState(t, f.s.Pool) != before {
		t.Fatal("wrong password hashed or changed business state")
	}
	if credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM credential_limit_buckets WHERE kind='actor' AND tat>clock_timestamp()+interval '9 seconds'") != "1" {
		t.Fatal("self-change did not reserve two actor units")
	}
	output, err = f.s.MutateCredential(context.Background(), f.users["owner"], f.command("password.change"))
	credentialFailure(t, output, err, 429, "RATE_LIMITED")
	if hashes != 0 {
		t.Fatal("denied budget reached Hash")
	}
}

func TestPreparedViewerMayChangeOwnPassword(t *testing.T) {
	f := preparedFixture(t)
	output, err := f.s.MutateCredential(context.Background(), f.users["viewer"], f.command("password.change"))
	if err != nil || output == nil {
		t.Fatal("enabled viewer lost self-password access", err)
	}
	if credentialValue(t, f.s.Pool, "SELECT role FROM users WHERE id=$1", f.users["viewer"].UserID) != "viewer" ||
		credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM sessions WHERE user_id=$1", f.users["viewer"].UserID) != "0" {
		t.Fatal("self-password changed role or retained sessions")
	}
}

func TestPreparedSharedReplicaBudgets(t *testing.T) {
	f := preparedFixture(t)
	other := f.replica(t)
	calls := 0
	hash := func(string) string { calls++; return "controlled-replica-hash" }
	f.s.passwordHash = hash
	other.passwordHash = hash
	for i := 0; i < 3; i++ {
		s := f.s
		if i%2 == 1 {
			s = other
		}
		if _, err := s.MutateCredential(context.Background(), f.users["admin"], f.command("user.create")); err != nil {
			t.Fatal(err)
		}
	}
	output, err := other.MutateCredential(context.Background(), f.users["admin"], f.command("user.create"))
	credentialFailure(t, output, err, 429, "RATE_LIMITED")
	if calls != 3 {
		t.Fatal("shared actor budget bypassed")
	}
	command := f.command("user.create")
	command.Client = netip.MustParseAddr("192.0.2.20")
	if _, err = other.MutateCredential(context.Background(), f.users["second"], command); err != nil {
		t.Fatal("independent actor/client denied", err)
	}
	credentialExec(t, f.s.Pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()+interval '1 hour',expires_at=clock_timestamp()+interval '2 hours' WHERE kind='client'")
	command = f.command("password.change")
	output, err = f.s.MutateCredential(context.Background(), f.users["owner"], command)
	credentialFailure(t, output, err, 429, "RATE_LIMITED")
	if calls != 4 || credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM credential_limit_buckets WHERE kind='actor'") != "2" {
		t.Fatal("client denial reached actor allocation/work")
	}
}

func TestPreparedDependenciesAndAuthorization(t *testing.T) {
	f := preparedFixture(t)
	ctx := context.Background()
	command := f.command("user.create")
	before := credentialState(t, f.s.Pool)
	for _, s := range []Service{{Pool: f.s.Pool}, {Pool: f.s.Pool, Limits: f.s.Limits}, {Pool: f.s.Pool, Work: f.s.Work}} {
		out, err := s.MutateCredential(ctx, f.users["admin"], command)
		credentialFailure(t, out, err, 503, "DEPENDENCY_UNAVAILABLE")
		out, err = s.MutateCredential(ctx, identity.Principal{}, command)
		credentialFailure(t, out, err, 401, "UNAUTHORIZED")
		out, err = s.MutateCredential(ctx, f.users["viewer"], command)
		credentialFailure(t, out, err, 403, "FORBIDDEN")
		if _, _, err = s.Page(ctx, f.users["admin"], "users", 0, 20); err != nil {
			t.Fatal("missing work dependency disabled reads", err)
		}
	}
	invalid := command
	invalid.Operation = "agent.update"
	out, err := f.s.MutateCredential(ctx, f.users["admin"], invalid)
	credentialFailure(t, out, err, 400, "INVALID_REQUEST")
	credentialExec(t, f.s.Pool, "ALTER TABLE credential_limit_buckets RENAME TO unavailable_credential_buckets")
	out, err = f.s.MutateCredential(ctx, f.users["admin"], command)
	credentialFailure(t, out, err, 503, "DEPENDENCY_UNAVAILABLE")
	if credentialState(t, f.s.Pool) != before {
		t.Fatal("denied dependency changed business state")
	}
}
