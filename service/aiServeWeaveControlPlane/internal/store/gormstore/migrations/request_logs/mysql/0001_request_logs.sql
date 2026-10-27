CREATE TABLE request_logs (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 key_display VARCHAR(64) NOT NULL,
 endpoint VARCHAR(16) NOT NULL,
 status_code SMALLINT NOT NULL,
 outcome VARCHAR(24) NOT NULL,
 duration_ms BIGINT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX idx_request_logs_tenant_created (tenant_id, created_at, id),
 INDEX idx_request_logs_tenant_outcome_created (tenant_id, outcome, created_at, id)
) ENGINE=InnoDB;
