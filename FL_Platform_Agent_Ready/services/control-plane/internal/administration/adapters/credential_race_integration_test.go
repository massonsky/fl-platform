//go:build integration

package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func TestPreparedWorkHasNoGuardAndRechecksActor(t *testing.T) {
	cases := []struct {
		op, change string
		status     int
		code       string
	}{
		{"user.create", "logout", 401, "UNAUTHORIZED"}, {"user.update", "disabled", 401, "UNAUTHORIZED"},
		{"password.change", "logout", 401, "UNAUTHORIZED"}, {"user.create", "demoted", 403, "FORBIDDEN"},
		{"user.update", "demoted", 403, "FORBIDDEN"}, {"password.change", "demoted", 409, "VERSION_CONFLICT"},
	}
	for _, tc := range cases {
		t.Run(tc.op+"/"+tc.change, func(t *testing.T) {
			f := preparedFixture(t)
			actor := f.users["admin"]
			if tc.op == "password.change" {
				actor = f.users["owner"]
			}
			resume, done := pauseCredential(t, f.s, actor, f.command(tc.op), context.Background(), "hash")
			probe, cancel := context.WithTimeout(context.Background(), time.Second)
			err := db.Transaction(probe, f.s.Pool, func(tx pgx.Tx) error { return authorization.Read(probe, tx) })
			cancel()
			if err != nil {
				t.Fatal("paused Hash blocked reader", err)
			}
			switch tc.change {
			case "logout":
				credentialWrite(t, f.s.Pool, "DELETE FROM sessions WHERE token_hash=$1", actor.SessionHash)
			case "disabled":
				credentialWrite(t, f.s.Pool, "UPDATE users SET disabled_at=clock_timestamp() WHERE id=$1", actor.UserID)
			case "demoted":
				credentialWrite(t, f.s.Pool, "UPDATE users SET role='viewer' WHERE id=$1", actor.UserID)
			}
			before := credentialState(t, f.s.Pool)
			resume()
			result := awaitCredential(t, done)
			credentialFailure(t, result.output, result.err, tc.status, tc.code)
			if credentialState(t, f.s.Pool) != before {
				t.Fatal("stale actor changed business state")
			}
		})
	}
}

func TestPreparedResetComparesTargetSnapshot(t *testing.T) {
	changes := map[string]string{
		"password":  "UPDATE users SET password_hash='concurrent-writer-hash' WHERE id=$1",
		"null-hash": "UPDATE users SET password_hash=NULL WHERE id=$1",
		"role":      "UPDATE users SET role='viewer' WHERE id=$1",
		"disabled":  "UPDATE users SET disabled_at=clock_timestamp() WHERE id=$1",
	}
	for name, sql := range changes {
		t.Run(name, func(t *testing.T) {
			f := preparedFixture(t)
			command := f.command("user.update")
			resume, done := pauseCredential(t, f.s, f.users["admin"], command, context.Background(), "hash")
			credentialWrite(t, f.s.Pool, sql, command.Target)
			before := credentialState(t, f.s.Pool)
			resume()
			result := awaitCredential(t, done)
			credentialFailure(t, result.output, result.err, 409, "VERSION_CONFLICT")
			if credentialState(t, f.s.Pool) != before {
				t.Fatal("stale snapshot overwrote concurrent state")
			}
		})
	}
}

func TestPreparedCancelledVerifyRetainsPermitAndSkipsHash(t *testing.T) {
	f := preparedFixture(t)
	hashes := 0
	f.s.passwordHash = func(string) string { hashes++; return "not-used" }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resume, done := pauseCredential(t, f.s, f.users["owner"], f.command("password.change"), ctx, "verify")
	cancel()
	before := credentialState(t, f.s.Pool)
	out, err := f.s.MutateCredential(context.Background(), f.users["admin"], f.command("user.create"))
	credentialFailure(t, out, err, 503, "DEPENDENCY_UNAVAILABLE")
	if credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM credential_limit_buckets WHERE kind='actor'") != "1" {
		t.Fatal("busy request reached actor work")
	}
	resume()
	result := awaitCredential(t, done)
	credentialFailure(t, result.output, result.err, 503, "DEPENDENCY_UNAVAILABLE")
	if hashes != 0 || credentialState(t, f.s.Pool) != before {
		t.Fatal("cancelled Verify hashed or committed")
	}
	// The actual return releases the shared permit, not the earlier cancellation.
	if err := f.s.Work.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal("permit retained after return", err)
	}
}

func TestPreparedFinalWaitUsesOriginalDeadline(t *testing.T) {
	f := preparedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resume, done := pauseCredential(t, f.s, f.users["admin"], f.command("user.create"), ctx, "hash")
	hold, err := f.s.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if err = authorization.Write(context.Background(), hold); err != nil {
		t.Fatal(err)
	}
	before := credentialState(t, f.s.Pool)
	start := time.Now()
	resume()
	result := awaitCredential(t, done)
	credentialFailure(t, result.output, result.err, 503, "DEPENDENCY_UNAVAILABLE")
	if time.Since(start) > 2*time.Second || credentialState(t, f.s.Pool) != before {
		t.Fatal("final transaction reset deadline or committed")
	}
}

func TestPreparedFinalExpiryUsesWallTime(t *testing.T) {
	f := preparedFixture(t)
	actor := f.users["admin"]
	resume, done := pauseCredential(t, f.s, actor, f.command("user.create"), context.Background(), "hash")
	credentialWrite(t, f.s.Pool, "UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1", actor.SessionHash)
	before := credentialState(t, f.s.Pool)
	resume()
	result := awaitCredential(t, done)
	credentialFailure(t, result.output, result.err, 401, "UNAUTHORIZED")
	if credentialState(t, f.s.Pool) != before {
		t.Fatal("expired final session committed")
	}
}
