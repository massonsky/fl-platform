-- ADR-0009 / AI-01. Additive; no changes to FL data or identities.
CREATE TABLE ai_providers (
  id uuid PRIMARY KEY,
  owner_id uuid NOT NULL REFERENCES users(id),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  base_url text NOT NULL CHECK (length(base_url) <= 512),
  encrypted_secret bytea NOT NULL,
  key_version text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
  last_probe_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_providers_owner ON ai_providers(owner_id, created_at, id);
