CREATE TABLE ai_conversations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id uuid NOT NULL REFERENCES users(id),
 provider_id uuid NOT NULL REFERENCES ai_providers(id),
 model text NOT NULL CHECK (octet_length(model) BETWEEN 1 AND 256),
 title text NOT NULL DEFAULT 'Новый диалог',
 consent_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz
);
CREATE INDEX ai_conversations_owner ON ai_conversations(owner_id,created_at DESC) WHERE deleted_at IS NULL;
CREATE TABLE ai_generations (
 owner_id uuid NOT NULL REFERENCES users(id),
 request_id uuid NOT NULL,
 conversation_id uuid NOT NULL REFERENCES ai_conversations(id),
 ordinal bigint GENERATED ALWAYS AS IDENTITY,
 fingerprint text NOT NULL,
 input text NOT NULL,
 content text NOT NULL DEFAULT '',
 status text NOT NULL CHECK (status IN ('streaming','completed','failed','cancelled','interrupted')),
 error_code text NOT NULL DEFAULT '',
 finish_reason text NOT NULL DEFAULT '',
 usage jsonb,
 provider_revision integer NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 deadline timestamptz NOT NULL DEFAULT now()+interval '120 seconds',
 PRIMARY KEY(owner_id,request_id)
);
CREATE UNIQUE INDEX ai_one_active_generation ON ai_generations(owner_id) WHERE status='streaming';
CREATE INDEX ai_generations_conversation ON ai_generations(conversation_id,ordinal);
CREATE INDEX ai_generations_rate ON ai_generations(owner_id,created_at DESC);
