-- COM-04A1S foundation. No legacy clients, keys, tasks or manifests are changed.
CREATE TABLE tenant_agents (
  id uuid PRIMARY KEY,
  organization_id uuid NOT NULL REFERENCES organizations(id),
  registry_slot smallint NOT NULL,
  name text NOT NULL,
  state text NOT NULL DEFAULT 'pending',
  revision integer NOT NULL DEFAULT 1,
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT tenant_agents_slot CHECK (registry_slot BETWEEN 1 AND 50),
  CONSTRAINT tenant_agents_name CHECK (char_length(name) BETWEEN 1 AND 120 AND length(btrim(name))>0 AND name !~ '[[:cntrl:]]'),
  CONSTRAINT tenant_agents_state CHECK (state IN ('pending','active','disabled')),
  CONSTRAINT tenant_agents_revision CHECK (revision>0),
  CONSTRAINT tenant_agents_times CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at>=created_at),
  UNIQUE(id,organization_id),
  UNIQUE(organization_id,registry_slot),
  FOREIGN KEY(organization_id,created_by) REFERENCES organization_memberships(organization_id,user_id)
);

-- registry_slot is a storage-capacity slot, never a model/data shard or client_index.
CREATE FUNCTION tenant_agent_immutable_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.organization_id IS DISTINCT FROM OLD.organization_id
     OR NEW.created_by IS DISTINCT FROM OLD.created_by THEN
    RAISE EXCEPTION 'immutable agent ownership' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER tenant_agent_immutable_owner BEFORE UPDATE ON tenant_agents
FOR EACH ROW EXECUTE FUNCTION tenant_agent_immutable_owner();

CREATE TABLE agent_issue_budgets (
  organization_id uuid PRIMARY KEY REFERENCES organizations(id),
  window_started_at timestamptz NOT NULL,
  used_units smallint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT agent_issue_budgets_units CHECK (used_units BETWEEN 0 AND 20 AND used_units%2=0),
  CONSTRAINT agent_issue_budgets_times CHECK (isfinite(window_started_at) AND isfinite(updated_at)
    AND date_trunc('hour',window_started_at AT TIME ZONE 'UTC')=window_started_at AT TIME ZONE 'UTC'
    AND updated_at>=window_started_at)
);

CREATE TABLE agent_enrollments (
  agent_id uuid PRIMARY KEY,
  organization_id uuid NOT NULL,
  enrollment_id uuid NOT NULL UNIQUE,
  digest bytea NOT NULL UNIQUE,
  issuer_user_id uuid NOT NULL,
  generation integer NOT NULL,
  budget_window timestamptz NOT NULL,
  reserved_units smallint NOT NULL DEFAULT 2,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  revoked_at timestamptz,
  CONSTRAINT agent_enrollments_digest CHECK (octet_length(digest)=32),
  CONSTRAINT agent_enrollments_generation CHECK (generation>0),
  CONSTRAINT agent_enrollments_reservation CHECK (reserved_units=2 AND isfinite(budget_window)
    AND date_trunc('hour',budget_window AT TIME ZONE 'UTC')=budget_window AT TIME ZONE 'UTC'
    AND budget_window<=created_at AND created_at<budget_window+interval '1 hour'),
  CONSTRAINT agent_enrollments_times CHECK (isfinite(created_at) AND isfinite(expires_at)
    AND expires_at>created_at AND expires_at<=created_at+interval '15 minutes'
    AND (consumed_at IS NULL OR (isfinite(consumed_at) AND consumed_at>=created_at AND consumed_at<expires_at))
    AND (revoked_at IS NULL OR (isfinite(revoked_at) AND revoked_at>=created_at))
    AND NOT (consumed_at IS NOT NULL AND revoked_at IS NOT NULL)),
  FOREIGN KEY(agent_id,organization_id) REFERENCES tenant_agents(id,organization_id),
  FOREIGN KEY(organization_id,issuer_user_id) REFERENCES organization_memberships(organization_id,user_id)
);

CREATE TABLE agent_credentials (
  agent_id uuid PRIMARY KEY,
  organization_id uuid NOT NULL,
  credential_id uuid NOT NULL UNIQUE,
  digest bytea NOT NULL UNIQUE,
  generation integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  CONSTRAINT agent_credentials_digest CHECK (octet_length(digest)=32),
  CONSTRAINT agent_credentials_generation CHECK (generation>0),
  -- 720 hours is an elapsed duration, independent of session timezone and DST.
  CONSTRAINT agent_credentials_times CHECK (isfinite(created_at) AND isfinite(expires_at)
    AND expires_at>created_at AND expires_at<=created_at+interval '720 hours'
    AND (revoked_at IS NULL OR (isfinite(revoked_at) AND revoked_at>=created_at))),
  FOREIGN KEY(agent_id,organization_id) REFERENCES tenant_agents(id,organization_id)
);

CREATE TABLE agent_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  organization_id uuid NOT NULL,
  agent_id uuid NOT NULL,
  actor_kind text NOT NULL,
  actor_user_id uuid REFERENCES users(id),
  enrollment_id uuid,
  action text NOT NULL,
  agent_revision integer NOT NULL,
  credential_generation integer,
  before_state text,
  after_state text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT agent_events_actor CHECK ((actor_kind='user' AND actor_user_id IS NOT NULL AND enrollment_id IS NULL)
    OR (actor_kind='enrollment' AND actor_user_id IS NULL AND enrollment_id IS NOT NULL)),
  CONSTRAINT agent_events_action CHECK (action IN ('agent_created','enrollment_issued','credential_issued','credential_revoked','agent_disabled','agent_enabled')),
  CONSTRAINT agent_events_revision CHECK (agent_revision>0 AND (credential_generation IS NULL OR credential_generation>0)),
  CONSTRAINT agent_events_states CHECK ((before_state IS NULL OR before_state IN ('pending','active','disabled'))
    AND after_state IN ('pending','active','disabled')),
  CONSTRAINT agent_events_time CHECK (isfinite(created_at)),
  FOREIGN KEY(agent_id,organization_id) REFERENCES tenant_agents(id,organization_id)
);
-- Typed audit only: no arbitrary JSON, credential digest, plaintext or network/data path.
CREATE INDEX agent_events_scope ON agent_events(organization_id,created_at DESC,id);
