CREATE TABLE response_turns (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 previous_response_id VARCHAR(64) NOT NULL DEFAULT '',
 model VARCHAR(128) NOT NULL DEFAULT '',
 messages TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX idx_response_turns_tenant (tenant_id, created_at, id)
) ENGINE=InnoDB;
