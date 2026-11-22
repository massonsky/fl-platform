CREATE TABLE ai_assistants (
 id uuid PRIMARY KEY,
 owner_id uuid NOT NULL REFERENCES users(id),
 provider_id uuid NOT NULL REFERENCES ai_providers(id),
 name text NOT NULL,
 model text NOT NULL,
 instruction text NOT NULL CHECK(octet_length(instruction) BETWEEN 1 AND 16384),
 enabled boolean NOT NULL DEFAULT true,
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_assistants_owner ON ai_assistants(owner_id,updated_at DESC);
ALTER TABLE ai_conversations
 ADD COLUMN assistant_id uuid REFERENCES ai_assistants(id),
 ADD COLUMN assistant_revision integer,
 ADD COLUMN assistant_name text NOT NULL DEFAULT '',
 ADD COLUMN system_instruction text NOT NULL DEFAULT '';
