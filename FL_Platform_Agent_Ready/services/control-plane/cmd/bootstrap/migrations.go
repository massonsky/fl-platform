package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations("/migrations")
	if err != nil {
		return err
	}
	return db.Transaction(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726182901)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
		if err != nil {
			return err
		}
		applied, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		pending, err := pendingMigrations(migrations, applied)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			var baselineExists bool
			if err := tx.QueryRow(ctx, "SELECT to_regclass('public.users') IS NOT NULL").Scan(&baselineExists); err != nil {
				return err
			}
			if baselineExists {
				return fmt.Errorf("untracked database schema: users exists with an empty migration ledger; reconcile schema before bootstrap")
			}
		}
		for _, migration := range pending {
			if _, err := tx.Exec(ctx, migration.sql); err != nil {
				return fmt.Errorf("apply migration %s (transaction rolled back): %w", migration.name, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", migration.name); err != nil {
				return err
			}
		}
		return nil
	})
}
