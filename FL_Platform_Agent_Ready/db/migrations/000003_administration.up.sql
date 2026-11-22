-- Additive working-platform administration. Existing projects remain owner-scoped.
ALTER TABLE users ADD COLUMN disabled_at timestamptz;
CREATE TABLE administration_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_id uuid NOT NULL REFERENCES users(id),
  target_id uuid NOT NULL,
  action text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX administration_events_created ON administration_events(created_at DESC,id);
CREATE TABLE administration_receipts (
  actor_id uuid NOT NULL REFERENCES users(id),
  operation text NOT NULL,
  idempotency_key text NOT NULL,
  fingerprint char(64) NOT NULL,
  response jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(actor_id,operation,idempotency_key)
);
