-- Forward extension for the MVP runtime; 000001 remains immutable.
ALTER TABLE users ADD COLUMN password_hash text;
CREATE TABLE sessions (
  token_hash char(64) PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id),
  expires_at timestamptz NOT NULL
);
CREATE TABLE http_receipts (
  user_id uuid NOT NULL REFERENCES users(id),
  operation text NOT NULL,
  idempotency_key text NOT NULL,
  fingerprint char(64) NOT NULL,
  response_status integer NOT NULL,
  response_body jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id,operation,idempotency_key)
);
ALTER TABLE clients ADD COLUMN shard_index integer UNIQUE CHECK (shard_index BETWEEN 0 AND 4);
ALTER TABLE rounds DROP CONSTRAINT rounds_status_check;
ALTER TABLE rounds ADD CHECK (status IN
 ('planned','dispatching','collecting','aggregating','evaluating','completed','failed','cancelled'));
ALTER TABLE rounds ADD COLUMN aggregation_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE rounds ADD COLUMN aggregation_owner uuid;
ALTER TABLE rounds ADD COLUMN aggregation_until timestamptz;
ALTER TABLE rounds ADD COLUMN frozen_update_ids uuid[];
ALTER TABLE rounds ADD COLUMN failure_code text;
ALTER TABLE tasks ADD COLUMN kind text NOT NULL DEFAULT 'train' CHECK (kind IN ('train','evaluate'));
ALTER TABLE tasks ADD COLUMN lease_id uuid;
ALTER TABLE tasks DROP CONSTRAINT tasks_round_id_client_id_attempt_key;
ALTER TABLE tasks ADD UNIQUE(round_id,client_id,kind,attempt);
ALTER TABLE artifacts ADD COLUMN uploaded_by_task uuid REFERENCES tasks(id);
ALTER TABLE artifacts ADD COLUMN upload_id text;
CREATE UNIQUE INDEX artifact_upload_receipt ON artifacts(uploaded_by_task,upload_id)
 WHERE uploaded_by_task IS NOT NULL;
ALTER TABLE updates ADD COLUMN request_hash char(64);
CREATE TABLE rpc_receipts (
  client_id uuid NOT NULL REFERENCES clients(id),
  operation text NOT NULL,
  idempotency_key text NOT NULL,
  request_hash char(64) NOT NULL,
  response_bytes bytea NOT NULL,
  PRIMARY KEY(client_id,operation,idempotency_key)
);
CREATE TABLE evaluations (
  round_id uuid PRIMARY KEY REFERENCES rounds(id),
  task_id uuid NOT NULL UNIQUE REFERENCES tasks(id),
  model_version_id uuid NOT NULL REFERENCES model_versions(id),
  validation_confusion jsonb NOT NULL,
  test_confusion jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE model_presets (
  id text PRIMARY KEY,
  config_json jsonb NOT NULL,
  schema_json jsonb NOT NULL,
  dataset_manifest jsonb NOT NULL,
  partition_manifest jsonb NOT NULL,
  runtime_manifest jsonb NOT NULL,
  initial_artifact_id uuid NOT NULL REFERENCES artifacts(id)
);
CREATE UNIQUE INDEX metrics_global_once ON metric_points(round_id,split,metric_name)
 WHERE client_id IS NULL;
ALTER TABLE metric_points ADD CHECK
 (metric_value > '-Infinity'::float8 AND metric_value < 'Infinity'::float8);
