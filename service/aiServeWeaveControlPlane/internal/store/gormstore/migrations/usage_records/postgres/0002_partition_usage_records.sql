DROP INDEX IF EXISTS idx_usage_records_tenant_model_created;
DROP INDEX IF EXISTS idx_usage_records_created;
ALTER TABLE usage_records DROP CONSTRAINT IF EXISTS usage_records_pkey;
ALTER TABLE usage_records RENAME TO usage_records_unpartitioned;
CREATE TABLE usage_records (
 id VARCHAR(64) NOT NULL,
 tenant_id VARCHAR(32) NOT NULL,
 model VARCHAR(128) NOT NULL,
 endpoint VARCHAR(24) NOT NULL,
 prompt_tokens BIGINT NOT NULL,
 completion_tokens BIGINT NOT NULL,
 total_tokens BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
/*aisw:partitions table=usage_records source=usage_records_unpartitioned*/;
INSERT INTO usage_records (id, tenant_id, model, endpoint, prompt_tokens, completion_tokens, total_tokens, created_at)
 SELECT id, tenant_id, model, endpoint, prompt_tokens, completion_tokens, total_tokens, created_at FROM usage_records_unpartitioned;
DROP TABLE usage_records_unpartitioned;
CREATE INDEX idx_usage_records_tenant_model_created ON usage_records (tenant_id, model, created_at);
CREATE INDEX idx_usage_records_created ON usage_records (created_at, id);
