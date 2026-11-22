// Package authorization coordinates security decisions across administration
// and organizations. Acquire a guard before any user, organization or member
// row lock, and keep it until the surrounding transaction commits or rolls back.
package authorization

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Keep the established administrative lock key for compatibility during rollout.
const guardKey int64 = 726182902

// OperationTimeout bounds SQL/JSON operations and security-writer waits. Never
// hold this guard across external I/O such as provider calls or S3 downloads.
const OperationTimeout = 10 * time.Second

// Read keeps a checked identity/membership stable while a protected read runs.
// It cannot be upgraded to Write after taking row locks.
func Read(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1)", guardKey)
	return err
}

// Write serializes identity and membership changes across all API replicas.
func Write(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", guardKey)
	return err
}
