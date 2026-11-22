-- ADR-0010 / COM-01 foundation only. Existing API authorization is unchanged.
CREATE TABLE organizations (
  id uuid PRIMARY KEY,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120 AND length(btrim(name)) > 0),
  kind text NOT NULL CHECK (kind IN ('personal','shared')),
  state text NOT NULL DEFAULT 'active' CHECK (state IN ('active','suspended')),
  created_by uuid NOT NULL REFERENCES users(id),
  personal_owner_id uuid UNIQUE REFERENCES users(id),
  revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((kind='personal' AND personal_owner_id IS NOT NULL) OR (kind='shared' AND personal_owner_id IS NULL))
);
CREATE INDEX organizations_created_by ON organizations(created_by,kind);

CREATE TABLE organization_memberships (
  organization_id uuid NOT NULL REFERENCES organizations(id),
  user_id uuid NOT NULL REFERENCES users(id),
  role text NOT NULL CHECK (role IN ('owner','admin','researcher','viewer')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended','revoked')),
  revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(organization_id,user_id)
);
CREATE INDEX organization_memberships_user ON organization_memberships(user_id,status,organization_id);

CREATE TABLE organization_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  organization_id uuid NOT NULL REFERENCES organizations(id),
  actor_id uuid NOT NULL REFERENCES users(id),
  target_user_id uuid REFERENCES users(id),
  action text NOT NULL CHECK (action IN ('organization_created','membership_changed','legacy_backfilled')),
  before_json jsonb NOT NULL DEFAULT '{}',
  after_json jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX organization_events_scope ON organization_events(organization_id,created_at DESC,id);

ALTER TABLE projects ADD COLUMN organization_id uuid REFERENCES organizations(id);
CREATE INDEX projects_organization ON projects(organization_id,id);

-- Preserve private project ownership. Viewer/disabled remain capped by users;
-- no AI records, manifests, roles, global slug constraints or old ACL are changed.
INSERT INTO organizations(id,name,kind,created_by,personal_owner_id)
SELECT gen_random_uuid(),'Личное пространство','personal',u.id,u.id
FROM users u WHERE EXISTS(SELECT 1 FROM projects p WHERE p.owner_id=u.id);
INSERT INTO organization_memberships(organization_id,user_id,role,status)
SELECT o.id,u.id,'owner',CASE WHEN u.disabled_at IS NULL THEN 'active' ELSE 'suspended' END
FROM organizations o JOIN users u ON u.id=o.personal_owner_id;
UPDATE projects p SET organization_id=o.id
FROM organizations o WHERE o.personal_owner_id=p.owner_id;
INSERT INTO organization_events(organization_id,actor_id,target_user_id,action,after_json)
SELECT o.id,o.created_by,o.personal_owner_id,'legacy_backfilled',
       jsonb_build_object('role',m.role,'status',m.status,'revision',m.revision)
FROM organizations o JOIN organization_memberships m ON m.organization_id=o.id AND m.user_id=o.personal_owner_id;
