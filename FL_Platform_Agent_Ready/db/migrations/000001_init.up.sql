-- DRAFT initial migration. Review and test in disposable PostgreSQL before production.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  external_subject text NOT NULL UNIQUE,
  display_name text NOT NULL,
  role text NOT NULL CHECK (role IN ('admin','researcher','viewer')),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id uuid NOT NULL REFERENCES users(id),
  slug text NOT NULL UNIQUE,
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE experiments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id),
  name text NOT NULL,
  config_json jsonb NOT NULL,
  config_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  experiment_id uuid NOT NULL REFERENCES experiments(id),
  created_by uuid NOT NULL REFERENCES users(id),
  status text NOT NULL CHECK (status IN ('draft','queued','running','completed','failed','cancelled')),
  manifest_json jsonb NOT NULL,
  manifest_hash text NOT NULL,
  failure_code text,
  requested_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz
);

CREATE TABLE clients (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  display_name text NOT NULL,
  identity_fingerprint text NOT NULL UNIQUE,
  software_version text NOT NULL,
  capabilities_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_seen_at timestamptz,
  status text NOT NULL DEFAULT 'registered' CHECK (status IN ('registered','online','offline','disabled'))
);

CREATE TABLE rounds (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id uuid NOT NULL REFERENCES runs(id),
  round_no integer NOT NULL CHECK (round_no >= 1),
  base_model_version bigint NOT NULL CHECK (base_model_version >= 0),
  status text NOT NULL CHECK (status IN ('planned','dispatching','collecting','aggregating','evaluating','completed','failed')),
  selected_count integer NOT NULL DEFAULT 0 CHECK (selected_count >= 0),
  accepted_count integer NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
  deadline_at timestamptz,
  started_at timestamptz,
  committed_at timestamptz,
  UNIQUE(run_id, round_no)
);

CREATE TABLE artifacts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  storage_key text NOT NULL UNIQUE,
  sha256_hex char(64) NOT NULL,
  size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
  codec text NOT NULL,
  model_schema_hash text,
  state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','committed','deleted')),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tasks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  round_id uuid NOT NULL REFERENCES rounds(id),
  client_id uuid NOT NULL REFERENCES clients(id),
  lease_token_hash char(64),
  lease_until timestamptz,
  attempt integer NOT NULL DEFAULT 1 CHECK (attempt >= 1),
  status text NOT NULL CHECK (status IN ('available','leased','submitted','expired','failed','cancelled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(round_id, client_id, attempt)
);

CREATE TABLE updates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  task_id uuid NOT NULL REFERENCES tasks(id),
  round_id uuid NOT NULL REFERENCES rounds(id),
  client_id uuid NOT NULL REFERENCES clients(id),
  base_model_version bigint NOT NULL,
  artifact_id uuid NOT NULL REFERENCES artifacts(id),
  sample_count bigint NOT NULL CHECK (sample_count > 0),
  idempotency_key text NOT NULL,
  status text NOT NULL CHECK (status IN ('received','accepted','rejected')),
  received_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(client_id, idempotency_key)
);
CREATE UNIQUE INDEX updates_one_accepted_per_client_round ON updates(round_id,client_id) WHERE status = 'accepted';

CREATE TABLE model_versions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id uuid NOT NULL REFERENCES runs(id),
  version_no bigint NOT NULL CHECK (version_no >= 0),
  produced_by_round_id uuid UNIQUE REFERENCES rounds(id),
  artifact_id uuid NOT NULL REFERENCES artifacts(id),
  model_schema_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(run_id, version_no)
);

CREATE TABLE metric_points (
  id bigserial PRIMARY KEY,
  run_id uuid NOT NULL REFERENCES runs(id),
  round_id uuid REFERENCES rounds(id),
  client_id uuid REFERENCES clients(id),
  split text NOT NULL CHECK (split IN ('train_local','validation_global','test_global')),
  metric_name text NOT NULL,
  metric_value double precision NOT NULL,
  measured_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_events (
  id bigserial PRIMARY KEY,
  run_id uuid REFERENCES runs(id),
  round_id uuid REFERENCES rounds(id),
  actor_id text NOT NULL,
  event_type text NOT NULL,
  safe_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX runs_by_experiment ON runs(experiment_id, requested_at DESC);
CREATE INDEX rounds_by_run ON rounds(run_id, round_no);
CREATE INDEX tasks_available ON tasks(status, lease_until, created_at);
CREATE INDEX metrics_run_round ON metric_points(run_id, round_id, metric_name);
CREATE INDEX audit_run_time ON audit_events(run_id, created_at);
