CREATE TABLE request_logs (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 key_display VARCHAR(64) NOT NULL,
 endpoint VARCHAR(16) NOT NULL,
 status_code SMALLINT NOT NULL,
 outcome VARCHAR(24) NOT NULL,
 duration_ms BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_request_logs_tenant_created ON request_logs (tenant_id, created_at, id);
CREATE INDEX idx_request_logs_tenant_outcome_created ON request_logs (tenant_id, outcome, created_at, id);
