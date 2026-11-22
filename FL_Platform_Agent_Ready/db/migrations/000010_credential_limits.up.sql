-- ADR-0010 / COM-03A1S. Shared budgets only; no identity/session backfill.
CREATE TABLE credential_limit_settings (
  singleton smallint PRIMARY KEY CHECK (singleton=1),
  policy_version text NOT NULL,
  key_fingerprint bytea CHECK (octet_length(key_fingerprint)=32),
  last_gc_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'
);
INSERT INTO credential_limit_settings(singleton,policy_version) VALUES
 (1,'credential-v1:client20/10;account5/5;actor10/3;cap16384;ret1800;gc500/60');

CREATE TABLE credential_limit_capacity (
  kind text PRIMARY KEY CHECK (kind IN ('client','account','actor')),
  used integer NOT NULL DEFAULT 0 CHECK (used BETWEEN 0 AND 16384)
);
INSERT INTO credential_limit_capacity(kind) VALUES ('client'),('account'),('actor');

CREATE TABLE credential_limit_buckets (
  kind text NOT NULL REFERENCES credential_limit_capacity(kind),
  digest bytea NOT NULL CHECK (octet_length(digest)=32),
  tat timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY(kind,digest),
  CHECK (isfinite(tat) AND isfinite(expires_at) AND expires_at>=tat)
);
CREATE INDEX credential_limit_expiry ON credential_limit_buckets(kind,expires_at,digest);
