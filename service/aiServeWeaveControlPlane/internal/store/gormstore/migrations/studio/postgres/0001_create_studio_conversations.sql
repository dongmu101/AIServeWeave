CREATE TABLE studio_conversations (
 id VARCHAR(40) NOT NULL PRIMARY KEY,
 tenant_id VARCHAR(40) NOT NULL,
 user_id VARCHAR(40) NOT NULL,
 title VARCHAR(200) NOT NULL,
 model VARCHAR(128) NOT NULL,
 system_prompt TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_studio_conversations_owner ON studio_conversations (tenant_id, user_id, updated_at);
