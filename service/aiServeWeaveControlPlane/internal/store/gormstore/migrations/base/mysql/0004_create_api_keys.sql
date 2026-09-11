CREATE TABLE IF NOT EXISTS api_keys (
    id VARCHAR(32) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    created_by VARCHAR(32) NOT NULL,
    name VARCHAR(128) NOT NULL,
    hash VARCHAR(64) NOT NULL,
    display VARCHAR(32) NOT NULL,
    status VARCHAR(16) NOT NULL,
    expires_at DATETIME(3),
    last_used_at DATETIME(3),
    revoked_at DATETIME(3),
    created_at DATETIME(3),
    updated_at DATETIME(3)
) ENGINE=InnoDB;
