CREATE TABLE IF NOT EXISTS agent_install_codes (
    code_hash VARCHAR(64) PRIMARY KEY,
    node_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(32) NOT NULL,
    registry_addr VARCHAR(255) NOT NULL,
    gateway_addr VARCHAR(255) NOT NULL,
    created_by VARCHAR(32) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    redeemed_at DATETIME(6) NULL
) ENGINE=InnoDB;
