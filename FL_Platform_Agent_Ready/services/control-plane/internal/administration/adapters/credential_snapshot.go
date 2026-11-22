package adapters

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// This private value never leaves the operation or enters a receipt/response.
// CAS compares exact stored values; it does not claim a revision/ABA guarantee.
type credentialSnapshot struct {
	actor, target, role string
	password            pgtype.Text
	disabled            pgtype.Timestamptz
}

func credentialReceipt(ctx context.Context, tx pgx.Tx, actor, op, key, fingerprint string) (json.RawMessage, error) {
	var old string
	var output json.RawMessage
	err := tx.QueryRow(ctx, `SELECT fingerprint,response FROM administration_receipts
 WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3`, actor, op, key).Scan(&old, &output)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if old != fingerprint {
		return nil, Failure{409, "IDEMPOTENCY_CONFLICT"}
	}
	return output, nil
}

func snapshotCredential(ctx context.Context, tx pgx.Tx, actor string, command CredentialMutation) (credentialSnapshot, json.RawMessage, error) {
	snapshot := credentialSnapshot{actor: actor, target: command.Target}
	replay, err := credentialReceipt(ctx, tx, actor, command.Operation, command.Key, command.Fingerprint)
	if err != nil || replay != nil {
		return snapshot, replay, err
	}
	if command.Operation == "user.create" {
		return snapshot, nil, nil
	}
	if command.Operation == "password.change" {
		snapshot.target = actor
	}
	err = tx.QueryRow(ctx, `SELECT password_hash,role,disabled_at FROM users WHERE id=$1`, snapshot.target).
		Scan(&snapshot.password, &snapshot.role, &snapshot.disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		err = Failure{404, "NOT_FOUND"}
	}
	return snapshot, nil, err
}

// Requires authorization.Write and a fresh Current check. The receipt is checked
// before this function: a committed identical request may already have changed
// the target. A valid replay must not be mistaken for a stale new mutation.
func (snapshot credentialSnapshot) matches(ctx context.Context, tx pgx.Tx, actor, op, target string) error {
	if actor != snapshot.actor {
		return Failure{401, "UNAUTHORIZED"}
	}
	if op == "user.create" {
		return nil
	}
	if op == "password.change" {
		target = actor
	}
	if target != snapshot.target {
		return Failure{409, "VERSION_CONFLICT"}
	}
	var same bool
	err := tx.QueryRow(ctx, `SELECT password_hash IS NOT DISTINCT FROM $2
 AND role=$3 AND disabled_at IS NOT DISTINCT FROM $4 FROM users WHERE id=$1 FOR UPDATE`,
		target, snapshot.password, snapshot.role, snapshot.disabled).Scan(&same)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !same {
		return Failure{409, "VERSION_CONFLICT"}
	}
	return err
}
