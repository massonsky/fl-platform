package credentials

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// begin uses a single wall-time budget, also bounded for callers without a deadline.
func (s *Store) begin(ctx context.Context) (context.Context, pgx.Tx, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	if s == nil || s.pool == nil || len(s.fingerprint) != 32 {
		cancel()
		return ctx, nil, func() {}, unavailable()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		cancel()
		return ctx, nil, func() {}, unavailable()
	}
	close := func() { _ = tx.Rollback(ctx); cancel() }
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='500ms'; SET LOCAL statement_timeout='1500ms'"); err == nil {
		err = s.ready(ctx, tx)
	}
	if err != nil {
		close()
		return ctx, nil, func() {}, unavailable()
	}
	return ctx, tx, close, nil
}

func (s *Store) consume(ctx context.Context, p policy, value string, units int) error {
	if units < 1 || units > 2 {
		return invalid()
	}
	ctx, tx, close, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer close()
	digest := s.digest(p.kind, value)
	tat, found, err := lockedBucket(ctx, tx, p.kind, digest)
	if err != nil {
		return unavailable()
	}
	if !found {
		// No bucket lock is held. Allocation order is always capacity -> bucket.
		var used int
		if err = tx.QueryRow(ctx, "SELECT used FROM public.credential_limit_capacity WHERE kind=$1 FOR UPDATE", p.kind).Scan(&used); err != nil {
			return unavailable()
		}
		tat, found, err = lockedBucket(ctx, tx, p.kind, digest)
		if err != nil {
			return unavailable()
		}
		if !found {
			if used < 0 || used >= bucketCap {
				return unavailable()
			}
			if _, err = tx.Exec(ctx, "UPDATE public.credential_limit_capacity SET used=used+1 WHERE kind=$1", p.kind); err != nil {
				return unavailable()
			}
		}
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return unavailable()
	}
	if tat.Before(now) {
		tat = now
	}
	next := tat.Add(time.Duration(units) * p.interval)
	if wait := next.Sub(now.Add(time.Duration(p.burst) * p.interval)); wait > 0 {
		// Sub may saturate after a large database clock rollback. Avoid overflowing
		// the duration while rounding Retry-After up to whole seconds.
		retry := wait / time.Second
		if wait%time.Second != 0 {
			retry++
		}
		return Failure{429, "RATE_LIMITED", int(retry)}
	}
	if found {
		_, err = tx.Exec(ctx, "UPDATE public.credential_limit_buckets SET tat=$3,expires_at=$4 WHERE kind=$1 AND digest=$2", p.kind, digest, next, next.Add(retention))
	} else {
		_, err = tx.Exec(ctx, "INSERT INTO public.credential_limit_buckets(kind,digest,tat,expires_at) VALUES($1,$2,$3,$4)", p.kind, digest, next, next.Add(retention))
	}
	if err != nil || tx.Commit(ctx) != nil {
		return unavailable()
	}
	return nil
}

func lockedBucket(ctx context.Context, tx pgx.Tx, kind string, digest []byte) (time.Time, bool, error) {
	var tat time.Time
	err := tx.QueryRow(ctx, "SELECT tat FROM public.credential_limit_buckets WHERE kind=$1 AND digest=$2 FOR UPDATE", kind, digest).Scan(&tat)
	if errors.Is(err, pgx.ErrNoRows) {
		return tat, false, nil
	}
	return tat, err == nil, err
}
