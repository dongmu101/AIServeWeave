DROP INDEX IF EXISTS idx_response_turns_tenant;
ALTER TABLE response_turns DROP CONSTRAINT IF EXISTS response_turns_pkey;
ALTER TABLE response_turns RENAME TO response_turns_unpartitioned;
CREATE TABLE response_turns (
 id VARCHAR(64) NOT NULL,
 tenant_id VARCHAR(32) NOT NULL,
 previous_response_id VARCHAR(64) NOT NULL DEFAULT '',
 model VARCHAR(128) NOT NULL DEFAULT '',
 messages TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
/*aisw:partitions table=response_turns source=response_turns_unpartitioned*/;
INSERT INTO response_turns (id, tenant_id, previous_response_id, model, messages, created_at)
 SELECT id, tenant_id, previous_response_id, model, messages, created_at FROM response_turns_unpartitioned;
DROP TABLE response_turns_unpartitioned;
CREATE INDEX idx_response_turns_tenant ON response_turns (tenant_id, created_at, id);
