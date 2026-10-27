CREATE TABLE response_turns (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 previous_response_id VARCHAR(64) NOT NULL DEFAULT '',
 model VARCHAR(128) NOT NULL DEFAULT '',
 messages TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_response_turns_tenant ON response_turns (tenant_id, created_at, id);
