package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

// Only this trusted provisioning path admits a newly inserted initial admin.
// A conflict/rerun must not promote an existing user, reset credentials, recreate
// a missing admission or undo an intentional revocation.
func bootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, passwordHash string) error {
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = authorization.Write(ctx, tx); err != nil {
		return err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO users(external_subject,display_name,role,password_hash)
 VALUES('admin','Исследователь','admin',$1) ON CONFLICT(external_subject) DO NOTHING RETURNING id`, passwordHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO legacy_lab_admissions(user_id,status,source,updated_by)
 VALUES($1,'active','bootstrap',$1)`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO legacy_lab_admission_events(target_user_id,actor_id,action,after_json,reason)
 VALUES($1,$1,'bootstrap_admitted','{"status":"active","revision":1,"source":"bootstrap"}',
 'Trusted bootstrap: newly inserted initial administrator')`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
