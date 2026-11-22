//go:build integration

package adapters

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPreparedConcurrentReplicasReceiptBeforeCAS(t *testing.T) {
	for _, op := range []string{"user.create", "user.update"} {
		for _, conflict := range []bool{false, true} {
			name := op + "/replay"
			if conflict {
				name = op + "/conflict"
			}
			t.Run(name, func(t *testing.T) {
				f := preparedFixture(t)
				other := f.replica(t)
				command := f.command(op)
				second := command
				if conflict {
					second.Fingerprint = strings.Repeat("a", 64)
					second.Input.Password = "different-fixture-password"
				}
				resumeA, doneA := pauseCredential(t, f.s, f.users["admin"], command, context.Background(), "hash")
				resumeB, doneB := pauseCredential(t, other, f.users["admin"], second, context.Background(), "hash")
				resumeA()
				first := awaitCredential(t, doneA)
				if first.err != nil {
					t.Fatal(first.err)
				}
				before := credentialState(t, f.s.Pool)
				resumeB()
				last := awaitCredential(t, doneB)
				if conflict {
					credentialFailure(t, last.output, last.err, 409, "IDEMPOTENCY_CONFLICT")
				} else {
					var a, b map[string]any
					if last.err != nil || json.Unmarshal(first.output, &a) != nil || json.Unmarshal(last.output, &b) != nil || a["id"] != b["id"] || b["saved"] != true {
						t.Fatal("concurrent replay did not return persisted identity", last.err)
					}
				}
				if credentialState(t, f.s.Pool) != before || credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM administration_events") != "1" || credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM administration_receipts") != "1" {
					t.Fatal("second finalization produced duplicate effects")
				}
				stored := credentialValue(t, f.s.Pool, "SELECT row_to_json(r)::text FROM administration_receipts r")
				if strings.Contains(stored, command.Input.Password) || strings.Contains(stored, "controlled-prepared-hash") || strings.Contains(stored, f.users["admin"].SessionHash) {
					t.Fatal("receipt contains credential material")
				}
			})
		}
	}
}

func TestPreparedFinalFailureReturnsNoDTOAndRollsBack(t *testing.T) {
	for _, failure := range []string{"audit", "commit"} {
		t.Run(failure, func(t *testing.T) {
			f := preparedFixture(t)
			command := f.command("user.update")
			before := credentialState(t, f.s.Pool)
			if failure == "audit" {
				credentialExec(t, f.s.Pool, "ALTER TABLE administration_events RENAME TO unavailable_admin_events")
			} else {
				credentialExec(t, f.s.Pool, `CREATE FUNCTION reject_prepared_commit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'isolated commit failure'; END$$;
 CREATE CONSTRAINT TRIGGER reject_prepared_commit AFTER INSERT ON administration_receipts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_prepared_commit()`)
			}
			output, err := f.s.MutateCredential(context.Background(), f.users["admin"], command)
			credentialFailure(t, output, err, 503, "DEPENDENCY_UNAVAILABLE")
			if failure == "audit" {
				credentialExec(t, f.s.Pool, "ALTER TABLE unavailable_admin_events RENAME TO administration_events")
			}
			if credentialState(t, f.s.Pool) != before {
				t.Fatal("failed finalization changed hash/sessions/audit/receipt")
			}
		})
	}
}

func TestPreparedRechecksLastAdminAfterWork(t *testing.T) {
	f := preparedFixture(t)
	command := f.command("user.update")
	command.Target = f.users["admin"].UserID
	command.Input.Role = "viewer"
	resume, done := pauseCredential(t, f.s, f.users["admin"], command, context.Background(), "hash")
	credentialWrite(t, f.s.Pool, "UPDATE users SET role='viewer' WHERE id=$1", f.users["second"].UserID)
	before := credentialState(t, f.s.Pool)
	resume()
	result := awaitCredential(t, done)
	credentialFailure(t, result.output, result.err, 409, "LAST_ADMIN")
	if credentialState(t, f.s.Pool) != before {
		t.Fatal("last administrator changed")
	}
}

func TestPreparedRechecksLastOrganizationOwnerAfterWork(t *testing.T) {
	f := preparedFixture(t)
	org := uuid.NewString()
	credentialExec(t, f.s.Pool, "INSERT INTO organizations(id,name,kind,state,created_by) VALUES($1,'Fixture','shared','active',$2)", org, f.users["owner"].UserID)
	credentialExec(t, f.s.Pool, "INSERT INTO organization_memberships(organization_id,user_id,role,status) VALUES($1,$2,'owner','active'),($1,$3,'owner','active')", org, f.users["owner"].UserID, f.users["second"].UserID)
	command := f.command("user.update")
	command.Input.Role = "viewer"
	resume, done := pauseCredential(t, f.s, f.users["admin"], command, context.Background(), "hash")
	credentialWrite(t, f.s.Pool, "UPDATE organization_memberships SET status='revoked' WHERE organization_id=$1 AND user_id=$2", org, f.users["second"].UserID)
	before := credentialState(t, f.s.Pool)
	resume()
	result := awaitCredential(t, done)
	credentialFailure(t, result.output, result.err, 409, "LAST_OWNER")
	if credentialState(t, f.s.Pool) != before {
		t.Fatal("last organization owner changed")
	}
}

func TestPreparedPersistedReplayRechecksCurrentPermission(t *testing.T) {
	f := preparedFixture(t)
	command := f.command("user.create")
	if _, err := f.s.MutateCredential(context.Background(), f.users["admin"], command); err != nil {
		t.Fatal(err)
	}
	credentialWrite(t, f.s.Pool, "UPDATE users SET role='viewer' WHERE id=$1", f.users["admin"].UserID)
	before := credentialState(t, f.s.Pool)
	out, err := (Service{Pool: f.s.Pool}).MutateCredential(context.Background(), f.users["admin"], command)
	credentialFailure(t, out, err, 403, "FORBIDDEN")
	credentialWrite(t, f.s.Pool, "DELETE FROM sessions WHERE token_hash=$1", f.users["admin"].SessionHash)
	before = credentialState(t, f.s.Pool)
	out, err = f.s.MutateCredential(context.Background(), f.users["admin"], command)
	credentialFailure(t, out, err, 401, "UNAUTHORIZED")
	if credentialValue(t, f.s.Pool, "SELECT count(*)::text FROM administration_receipts") != "1" || credentialState(t, f.s.Pool) != before {
		t.Fatal("rejected replay changed receipts")
	}
}
