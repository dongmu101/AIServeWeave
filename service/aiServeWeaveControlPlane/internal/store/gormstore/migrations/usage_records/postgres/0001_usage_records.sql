CREATE TABLE usage_records (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 model VARCHAR(128) NOT NULL,
 endpoint VARCHAR(24) NOT NULL,
 prompt_tokens BIGINT NOT NULL,
 completion_tokens BIGINT NOT NULL,
 total_tokens BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_usage_records_tenant_model_created ON usage_records (tenant_id, model, created_at);
CREATE INDEX idx_usage_records_created ON usage_records (created_at, id);
