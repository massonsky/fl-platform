package credentials

import (
	"bytes"
	"context"

	"github.com/jackc/pgx/v5"
)

// ready checks the bounded structure needed by every operation, including reads
// that do not create a bucket. Exact CHECK/index DDL is verified by rehearsal.
func (s *Store) ready(ctx context.Context, tx pgx.Tx) error {
	var complete bool
	err := tx.QueryRow(ctx, `SELECT bool_and(coalesce(c.relkind='r'
 AND a.atttypid=to_regtype(e.typ) AND a.attnotnull=e.required AND NOT a.attisdropped,false))
 FROM (VALUES
 ('credential_limit_settings','singleton','smallint',true),
 ('credential_limit_settings','policy_version','text',true),
 ('credential_limit_settings','key_fingerprint','bytea',false),
 ('credential_limit_settings','last_gc_at','timestamptz',true),
 ('credential_limit_capacity','kind','text',true),
 ('credential_limit_capacity','used','integer',true),
 ('credential_limit_buckets','kind','text',true),
 ('credential_limit_buckets','digest','bytea',true),
 ('credential_limit_buckets','tat','timestamptz',true),
 ('credential_limit_buckets','expires_at','timestamptz',true)
 ) AS e(rel,col,typ,required)
 LEFT JOIN pg_class c ON c.relname=e.rel AND c.relnamespace='public'::regnamespace
 LEFT JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=e.col`).Scan(&complete)
	if err != nil || !complete {
		return unavailable()
	}
	// A partial schema without a real, usable PK must not admit duplicate buckets.
	err = tx.QueryRow(ctx, `SELECT bool_and(EXISTS(
 SELECT 1 FROM pg_constraint k JOIN pg_index i ON i.indexrelid=k.conindid
 WHERE k.conrelid=to_regclass('public.'||e.rel) AND k.contype='p' AND k.convalidated
 AND i.indisvalid AND i.indisready AND NOT k.condeferrable
 AND (SELECT array_agg(a.attname::text ORDER BY ord) FROM unnest(k.conkey) WITH ORDINALITY AS cols(num,ord)
 JOIN pg_attribute a ON a.attrelid=k.conrelid AND a.attnum=cols.num)=e.cols))
 FROM (VALUES ('credential_limit_settings',ARRAY['singleton']),
 ('credential_limit_capacity',ARRAY['kind']),('credential_limit_buckets',ARRAY['kind','digest'])) AS e(rel,cols)`).Scan(&complete)
	if err != nil || !complete {
		return unavailable()
	}
	var version string
	var fingerprint []byte
	var singleton int
	rows, err := tx.Query(ctx, "SELECT singleton,policy_version,key_fingerprint FROM public.credential_limit_settings LIMIT 2")
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&singleton, &version, &fingerprint); err != nil {
			break
		}
	}
	rows.Close()
	if err != nil || rows.Err() != nil || count != 1 || singleton != 1 || version != policyVersion {
		return unavailable()
	}
	if fingerprint == nil {
		// Recheck under the first-use lock; a concurrent replica may have bound it.
		// GC uses this same settings -> capacity -> bucket order, never the reverse.
		if err = tx.QueryRow(ctx, "SELECT key_fingerprint FROM public.credential_limit_settings WHERE singleton=1 FOR UPDATE").Scan(&fingerprint); err != nil {
			return err
		}
		if fingerprint == nil {
			var nonempty bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.credential_limit_buckets)
 OR EXISTS(SELECT 1 FROM public.credential_limit_capacity WHERE used<>0)`).Scan(&nonempty); err != nil {
				return err
			}
			if nonempty {
				return unavailable()
			}
			if _, err = tx.Exec(ctx, "UPDATE public.credential_limit_settings SET key_fingerprint=$1 WHERE singleton=1", s.fingerprint); err != nil {
				return err
			}
			fingerprint = s.fingerprint
		}
	}
	if !bytes.Equal(fingerprint, s.fingerprint) {
		return unavailable()
	}
	rows, err = tx.Query(ctx, "SELECT kind,used FROM public.credential_limit_capacity ORDER BY kind LIMIT 4")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var kind string
		var used int
		if err = rows.Scan(&kind, &used); err != nil {
			break
		}
		if (kind != "client" && kind != "account" && kind != "actor") || used < 0 || used > bucketCap {
			err = unavailable()
			break
		}
		seen[kind] = true
	}
	rows.Close()
	if err != nil || rows.Err() != nil || len(seen) != 3 {
		return unavailable()
	}
	return nil
}
