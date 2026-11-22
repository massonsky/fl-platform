//go:build integration

package legacylab

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func TestAdmissionSessionRoleCASAndNoop(t *testing.T) {
	pool := labDatabase(t, 9)
	s, ctx := Service{pool}, context.Background()
	admin, target, viewer := labUser(t, pool, "admin"), labUser(t, pool, "researcher"), labUser(t, pool, "viewer")
	in := Change{Status: "active", Reason: "Operator reviewed access"}
	for _, p := range []identity.Principal{target, viewer} {
		_, err := s.Get(ctx, p, admin.UserID)
		labFailure(t, err, 403, "FORBIDDEN")
		_, err = s.Change(ctx, p, p.UserID, in)
		labFailure(t, err, 403, "FORBIDDEN")
	}
	for _, p := range []identity.Principal{{}, {UserID: admin.UserID}, {UserID: admin.UserID, SessionHash: target.SessionHash}} {
		before := labHash(t, pool, true)
		got, err := s.Change(ctx, p, target.UserID, in)
		labFailure(t, err, 401, "UNAUTHORIZED")
		if got != (Admission{}) || labHash(t, pool, true) != before {
			t.Fatal("invalid session returned data or mutated state")
		}
	}
	_, err := s.Get(ctx, admin, uuid.NewString())
	labFailure(t, err, 404, "NOT_FOUND")
	_, err = s.Change(ctx, admin, "bad-uuid", in)
	labFailure(t, err, 400, "INVALID_REQUEST")
	for _, input := range []Change{{"active", -1, "reason"}, {"invalid", 0, "reason"}, {"active", 0, ""}, {"active", 0, "bad\x00reason"}} {
		_, err = s.Change(ctx, admin, target.UserID, input)
		labFailure(t, err, 400, "INVALID_REQUEST")
	}
	granted, err := s.Change(ctx, admin, target.UserID, in)
	if err != nil || !granted.CanCreateLegacyWork || granted.Revision != 1 || granted.Source != "admin" {
		t.Fatal("grant failed", err)
	}
	if err = requireLab(t, pool, target); err != nil {
		t.Fatal(err)
	}
	before := labHash(t, pool, true)
	in.Revision = 1
	_, err = s.Change(ctx, admin, target.UserID, in)
	if err != nil || labHash(t, pool, true) != before {
		t.Fatal("current no-op changed revision/audit", err)
	}
	in.Revision = 0
	_, err = s.Change(ctx, admin, target.UserID, in)
	labFailure(t, err, 409, "VERSION_CONFLICT")
	if labHash(t, pool, true) != before {
		t.Fatal("stale replay changed state")
	}
	start, done := make(chan struct{}), make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := s.Change(ctx, admin, target.UserID, Change{"revoked", 1, "Withdraw future work"})
			done <- err
		}()
	}
	close(start)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-done
		var f Failure
		if err == nil {
			success++
		} else if errors.As(err, &f) && f.Code == "VERSION_CONFLICT" {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 || labScalar(t, pool, "SELECT count(*)::text FROM legacy_lab_admission_events") != "2" {
		t.Fatal("CAS race did not preserve one revision/audit")
	}
	labFailure(t, requireLab(t, pool, target), 403, "LEGACY_LAB_NOT_ADMITTED")
	view, err := s.Change(ctx, admin, viewer.UserID, in)
	if err != nil || view.Status != "active" || view.CanCreateLegacyWork {
		t.Fatal("metadata grant bypassed viewer cap", err)
	}
	labExec(t, pool, "UPDATE users SET disabled_at=now() WHERE id=$1", viewer.UserID)
	view, err = s.Get(ctx, admin, viewer.UserID)
	if err != nil || view.CanCreateLegacyWork {
		t.Fatal("metadata grant bypassed disabled cap", err)
	}
	labFailure(t, requireLab(t, pool, viewer), 401, "UNAUTHORIZED")
	labExec(t, pool, "UPDATE users SET role='viewer' WHERE id=$1", admin.UserID)
	_, err = s.Change(ctx, admin, target.UserID, Change{"active", 2, "stale admin"})
	labFailure(t, err, 403, "FORBIDDEN")
	labExec(t, pool, "DELETE FROM sessions WHERE user_id=$1", admin.UserID)
	_, err = s.Self(ctx, admin)
	labFailure(t, err, 401, "UNAUTHORIZED")
}

func TestAdmissionRollbackAndCommitFailureReturnNoDTO(t *testing.T) {
	pool := labDatabase(t, 9)
	s, ctx := Service{pool}, context.Background()
	admin, target := labUser(t, pool, "admin"), labUser(t, pool, "researcher")
	before := labHash(t, pool, true)
	for _, deferred := range []bool{false, true} {
		labExec(t, pool, "CREATE FUNCTION reject_lab_event() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'isolated audit failure'; END$$")
		trigger := "CREATE TRIGGER reject_lab_event AFTER INSERT ON legacy_lab_admission_events FOR EACH ROW EXECUTE FUNCTION reject_lab_event()"
		if deferred {
			trigger = "CREATE CONSTRAINT TRIGGER reject_lab_event AFTER INSERT ON legacy_lab_admission_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_lab_event()"
		}
		labExec(t, pool, trigger)
		got, err := s.Change(ctx, admin, target.UserID, Change{"active", 0, "Grant fixture"})
		labFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
		if got != (Admission{}) || labHash(t, pool, true) != before {
			t.Fatal("audit/commit failure exposed DTO or changed data")
		}
		labExec(t, pool, "DROP TRIGGER reject_lab_event ON legacy_lab_admission_events; DROP FUNCTION reject_lab_event()")
	}
}

func TestAdmissionExpiryAfterGuardAndBoundedHelper(t *testing.T) {
	pool := labDatabase(t, 9)
	s, ctx := Service{pool}, context.Background()
	admin, target := labUser(t, pool, "admin"), labUser(t, pool, "researcher")
	hold, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if err = authorization.Write(ctx, hold); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Self(waitCtx, admin); done <- err }()
	for {
		var waiting bool
		if err = pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=726182902
 AND NOT granted AND mode='ShareLock' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	labExec(t, pool, "UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1", admin.SessionHash)
	if err = hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	labFailure(t, <-done, 401, "UNAUTHORIZED")
	labExec(t, pool, "UPDATE sessions SET expires_at=clock_timestamp()+interval '1 hour' WHERE token_hash=$1", admin.SessionHash)
	labExec(t, pool, "CREATE FUNCTION slow_lab_event() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_sleep(15); RETURN NEW; END$$; CREATE TRIGGER slow_lab_event BEFORE INSERT ON legacy_lab_admission_events FOR EACH ROW EXECUTE FUNCTION slow_lab_event()")
	before := labHash(t, pool, true)
	started := time.Now()
	got, err := s.Change(ctx, admin, target.UserID, Change{"active", 0, "slow fixture"})
	labFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if time.Since(started) > authorization.OperationTimeout+2*time.Second || got != (Admission{}) || labHash(t, pool, true) != before {
		t.Fatal("helper escaped deadline or committed partial state")
	}
	probe, cancelProbe := context.WithTimeout(ctx, time.Second)
	defer cancelProbe()
	tx, err := pool.Begin(probe)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = authorization.Write(probe, tx); err != nil {
		t.Fatal("timed-out operation retained security guard", err)
	}
}
