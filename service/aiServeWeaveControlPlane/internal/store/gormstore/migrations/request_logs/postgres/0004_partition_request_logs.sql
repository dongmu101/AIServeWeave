DROP INDEX IF EXISTS idx_request_logs_tenant_created;
DROP INDEX IF EXISTS idx_request_logs_tenant_outcome_created;
DROP INDEX IF EXISTS idx_request_logs_created;
ALTER TABLE request_logs DROP CONSTRAINT IF EXISTS request_logs_pkey;
ALTER TABLE request_logs RENAME TO request_logs_unpartitioned;
CREATE TABLE request_logs (
 id VARCHAR(64) NOT NULL,
 tenant_id VARCHAR(32) NOT NULL,
 key_display VARCHAR(64) NOT NULL,
 endpoint VARCHAR(16) NOT NULL,
 status_code SMALLINT NOT NULL,
 outcome VARCHAR(24) NOT NULL,
 duration_ms BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 model VARCHAR(128) NOT NULL DEFAULT '',
 error_code VARCHAR(64) NOT NULL DEFAULT '',
 error_message VARCHAR(512) NOT NULL DEFAULT '',
 error_detail VARCHAR(1024) NOT NULL DEFAULT '',
 PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
/*aisw:partitions table=request_logs source=request_logs_unpartitioned*/;
INSERT INTO request_logs (id, tenant_id, key_display, endpoint, status_code, outcome, duration_ms, created_at, model, error_code, error_message, error_detail)
 SELECT id, tenant_id, key_display, endpoint, status_code, outcome, duration_ms, created_at, model, error_code, error_message, error_detail FROM request_logs_unpartitioned;
DROP TABLE request_logs_unpartitioned;
CREATE INDEX idx_request_logs_tenant_created ON request_logs (tenant_id, created_at, id);
CREATE INDEX idx_request_logs_tenant_outcome_created ON request_logs (tenant_id, outcome, created_at, id);
CREATE INDEX idx_request_logs_created ON request_logs (created_at, id);
