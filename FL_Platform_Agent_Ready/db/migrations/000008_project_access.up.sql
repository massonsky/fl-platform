-- ADR-0010 / COM-02B foundation. Not an HTTP authorization cutover.
ALTER TABLE projects
 ADD COLUMN created_by uuid REFERENCES users(id),
 ADD COLUMN access_mode text NOT NULL DEFAULT 'legacy_private' CHECK(access_mode IN ('legacy_private','organization')),
 ADD COLUMN visibility text NOT NULL DEFAULT 'private' CHECK(visibility IN ('private','organization')),
 ADD COLUMN acl_revision integer NOT NULL DEFAULT 1 CHECK(acl_revision>0),
 ADD CONSTRAINT projects_legacy_visibility CHECK(access_mode<>'legacy_private' OR visibility='private'),
 ADD CONSTRAINT projects_organization_required CHECK(access_mode<>'organization' OR (organization_id IS NOT NULL AND created_by IS NOT NULL));
UPDATE projects SET created_by=owner_id WHERE created_by IS NULL;
CREATE UNIQUE INDEX projects_id_organization_key ON projects(id,organization_id);
ALTER TABLE projects DROP CONSTRAINT projects_slug_key;
-- Preserve this name for the existing SLUG_CONFLICT mapper and legacy writers.
CREATE UNIQUE INDEX projects_slug_key ON projects(slug) WHERE access_mode='legacy_private';
CREATE UNIQUE INDEX projects_organization_slug_key ON projects(organization_id,slug) WHERE access_mode='organization';

CREATE TABLE project_members (
 project_id uuid NOT NULL,
 organization_id uuid NOT NULL,
 user_id uuid NOT NULL,
 role text NOT NULL CHECK(role IN ('researcher','viewer')),
 status text NOT NULL CHECK(status IN ('active','revoked')),
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 granted_by uuid NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,user_id),
 FOREIGN KEY(project_id,organization_id) REFERENCES projects(id,organization_id),
 FOREIGN KEY(organization_id,user_id) REFERENCES organization_memberships(organization_id,user_id)
);
CREATE INDEX project_members_user ON project_members(organization_id,user_id,status,project_id);
CREATE TABLE project_access_events (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL,
 organization_id uuid NOT NULL,
 actor_id uuid NOT NULL REFERENCES users(id),
 target_user_id uuid REFERENCES users(id),
 action text NOT NULL CHECK(action IN ('project_created','grant_changed','visibility_changed')),
 before_json jsonb NOT NULL DEFAULT '{}',
 after_json jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project_id,organization_id) REFERENCES projects(id,organization_id)
);
CREATE INDEX project_access_events_project ON project_access_events(project_id,created_at DESC,id);
