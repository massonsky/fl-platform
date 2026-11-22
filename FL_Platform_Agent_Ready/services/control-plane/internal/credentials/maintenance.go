package credentials

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const gcLock = 726182914

// Collect deletes at most 500 fully refilled, retained buckets. PostgreSQL owns
// the schedule across replicas; this method never starts goroutines or a queue.
func (s *Store) Collect(ctx context.Context) (int, error) {
	ctx, tx, close, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer close()
	var winner bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", gcLock).Scan(&winner); err != nil {
		return 0, unavailable()
	}
	if !winner {
		return 0, nil
	}
	var now, last time.Time
	// First-use initialization also locks settings before capacity. Keep that
	// order even after initialization; otherwise a waiting initializer can deadlock.
	if err = tx.QueryRow(ctx, "SELECT last_gc_at FROM public.credential_limit_settings WHERE singleton=1 FOR UPDATE").Scan(&last); err != nil {
		return 0, unavailable()
	}
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return 0, unavailable()
	}
	if now.Sub(last) < gcInterval {
		return 0, nil
	}
	// The schedule change rolls back together with deleted buckets on any failure.
	if _, err = tx.Exec(ctx, "UPDATE public.credential_limit_settings SET last_gc_at=$1 WHERE singleton=1", now); err != nil {
		return 0, unavailable()
	}
	total := 0
	for _, p := range policies {
		if total == gcBatch {
			break
		}
		count, err := collectKind(ctx, tx, p.kind, gcBatch-total)
		if err != nil {
			return 0, unavailable()
		}
		total += count
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, unavailable()
	}
	return total, nil
}

func collectKind(ctx context.Context, tx pgx.Tx, kind string, limit int) (int, error) {
	var used int
	if err := tx.QueryRow(ctx, "SELECT used FROM public.credential_limit_capacity WHERE kind=$1 FOR UPDATE", kind).Scan(&used); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `WITH expired AS (
 SELECT digest FROM public.credential_limit_buckets
 WHERE kind=$1 AND tat<=clock_timestamp() AND expires_at<=clock_timestamp()
 ORDER BY expires_at,digest LIMIT $2 FOR UPDATE SKIP LOCKED
 ) DELETE FROM public.credential_limit_buckets b USING expired e
 WHERE b.kind=$1 AND b.digest=e.digest
 AND b.tat<=clock_timestamp() AND b.expires_at<=clock_timestamp()
 RETURNING b.digest`, kind, limit)
	if err != nil {
		return 0, err
	}
	deleted, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return 0, err
	}
	if len(deleted) > used {
		return 0, unavailable()
	}
	_, err = tx.Exec(ctx, "UPDATE public.credential_limit_capacity SET used=used-$2 WHERE kind=$1", kind, len(deleted))
	return len(deleted), err
}
