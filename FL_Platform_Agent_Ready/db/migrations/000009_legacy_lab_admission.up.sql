-- ADR-0010 / COM-02D1A. Admission to new legacy work, not tenant/role access.
CREATE TABLE legacy_lab_admissions (
  user_id uuid PRIMARY KEY REFERENCES users(id),
  status text NOT NULL CHECK (status IN ('active','revoked')),
  revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
  source text NOT NULL CHECK (source IN ('legacy_backfill','bootstrap','admin')),
  updated_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE legacy_lab_admission_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  target_user_id uuid NOT NULL REFERENCES users(id),
  actor_id uuid REFERENCES users(id),
  action text NOT NULL CHECK (action IN ('legacy_backfilled','bootstrap_admitted','changed')),
  before_json jsonb NOT NULL DEFAULT '{}',
  after_json jsonb NOT NULL,
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 240 AND length(btrim(reason)) > 0),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX legacy_lab_admission_events_target ON legacy_lab_admission_events(target_user_id,created_at DESC,id);

-- Snapshot only: later identities do not inherit admission from role or tenure.
-- Disabled/viewer caps remain authoritative; this does not enable an identity.
INSERT INTO legacy_lab_admissions(user_id,status,source)
SELECT u.id,'active','legacy_backfill' FROM users u
WHERE u.role IN ('admin','researcher') OR EXISTS(
  SELECT 1 FROM projects p WHERE p.owner_id=u.id AND p.access_mode='legacy_private');
INSERT INTO legacy_lab_admission_events(target_user_id,action,after_json,reason)
SELECT user_id,'legacy_backfilled',jsonb_build_object('status',status,'revision',revision,'source',source),
       'Migration 000009: existing researcher/admin or legacy project owner'
FROM legacy_lab_admissions;
