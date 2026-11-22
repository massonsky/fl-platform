//go:build integration

package adapters

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func TestLoginDummyRecordsNeverAuthorizeAndWorkHoldsNoGuard(t *testing.T) {
	for _, kind := range []string{"unknown", "disabled", "malformed", "wrong"} {
		t.Run(kind, func(t *testing.T) {
			s, id := loginFixture(t)
			in := inputForLogin()
			switch kind {
			case "unknown":
				in.Username = "unknown"
			case "disabled":
				loginExec(t, s.Pool, "UPDATE users SET disabled_at=clock_timestamp() WHERE id=$1", id)
			case "malformed":
				loginExec(t, s.Pool, "UPDATE users SET password_hash='broken' WHERE id=$1", id)
			}
			calls := 0
			s.verifyPassword = func(_ string, hash string) bool {
				calls++
				if !validPasswordRecord(hash) {
					t.Error("dummy path skipped valid-format work")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				tx, err := s.Pool.Begin(ctx)
				if err != nil {
					t.Error(err)
					return false
				}
				defer tx.Rollback(ctx)
				if err = authorization.Write(ctx, tx); err != nil {
					t.Error("Verify held authorization guard", err)
				}
				if _, err = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", id); err != nil {
					t.Error("Verify held user row", err)
				}
				return kind != "wrong"
			}
			token, err := s.Login(context.Background(), in)
			assertLoginFailure(t, token, err, 401)
			if calls != 1 || loginSessions(t, s) != 0 {
				t.Fatal("denied identity skipped work or created a session")
			}
		})
	}
}

func TestLoginRevalidatesHashAndDisableAfterWork(t *testing.T) {
	for _, update := range []string{"disabled_at=clock_timestamp()", "password_hash='replacement'"} {
		t.Run(update, func(t *testing.T) {
			s, id := loginFixture(t)
			release, result := pausedLogin(t, &s, context.Background())
			tx, err := s.Pool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if err = authorization.Write(context.Background(), tx); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(context.Background(), "UPDATE users SET "+update+" WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			close(release)
			out := <-result
			assertLoginFailure(t, out.token, out.err, 401)
			if loginSessions(t, s) != 0 {
				t.Fatal("stale credential committed")
			}
		})
	}
}

func TestLoginSnapshotIdentityAndOldSessionLogoutAreDistinct(t *testing.T) {
	s, id := loginFixture(t)
	loginExec(t, s.Pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')", Hash("old-session"), id)
	release, result := pausedLogin(t, &s, context.Background())
	if err := s.Logout(context.Background(), "old-session"); err != nil {
		t.Fatal(err)
	}
	loginExec(t, s.Pool, "UPDATE users SET external_subject='renamed' WHERE id=$1", id)
	loginExec(t, s.Pool, "INSERT INTO users(external_subject,display_name,role,password_hash) VALUES('login-user','Other','researcher',$1)", dummyPasswordHash)
	close(release)
	out := <-result
	if out.err != nil || out.token == "" {
		t.Fatal(out.err)
	}
	user, err := s.Authenticate(context.Background(), out.token)
	if err != nil || user.ID != id {
		t.Fatal("finalization looked up mutable username", err)
	}
	if _, err = s.Authenticate(context.Background(), "old-session"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old logout lost")
	}
	// A later security writer revokes a session committed before it.
	tx, err := s.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = authorization.Write(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(context.Background(), out.token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("later writer did not revoke")
	}
}

func TestLoginDeadlineAndCommitFailureReturnNoToken(t *testing.T) {
	t.Run("final-guard-deadline", func(t *testing.T) {
		s, _ := loginFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		release, result := pausedLogin(t, &s, ctx)
		tx, err := s.Pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if err = authorization.Write(context.Background(), tx); err != nil {
			t.Fatal(err)
		}
		close(release)
		select {
		case out := <-result:
			assertLoginFailure(t, out.token, out.err, 503)
		case <-time.After(time.Second):
			t.Fatal("finalization restarted deadline")
		}
		if loginSessions(t, s) != 0 {
			t.Fatal("expired login committed")
		}
	})
	t.Run("deferred-commit-error", func(t *testing.T) {
		s, _ := loginFixture(t)
		s.verifyPassword = func(string, string) bool { return true }
		loginExec(t, s.Pool, `CREATE FUNCTION fail_login() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture commit failure'; END $$;
 CREATE CONSTRAINT TRIGGER fail_login AFTER INSERT ON sessions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_login()`)
		token, err := s.Login(context.Background(), inputForLogin())
		assertLoginFailure(t, token, err, 503)
		if loginSessions(t, s) != 0 {
			t.Fatal("failed commit left session")
		}
	})
}

func TestCancelledVerifyRetainsSharedPermitAndExistingAuthentication(t *testing.T) {
	s, id := loginFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	release, result := pausedLogin(t, &s, ctx)
	cancel()
	// A Store copy shares the process permit. It cannot acquire one on cancellation.
	token, err := s.Login(context.Background(), inputForLogin())
	assertLoginFailure(t, token, err, 503)
	close(release)
	out := <-result
	assertLoginFailure(t, out.token, out.err, 503)
	loginExec(t, s.Pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')", Hash("existing"), id)
	s.Limits = nil
	s.Work = nil
	token, err = s.Login(context.Background(), inputForLogin())
	assertLoginFailure(t, token, err, 503)
	if user, err := s.Authenticate(context.Background(), "existing"); err != nil || user.ID != id {
		t.Fatal("missing key broke existing authentication", err)
	}
	if err = s.Logout(context.Background(), "existing"); err != nil {
		t.Fatal(err)
	}
}
