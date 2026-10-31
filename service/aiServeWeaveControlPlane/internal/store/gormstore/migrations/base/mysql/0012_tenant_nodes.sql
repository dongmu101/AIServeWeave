CREATE TABLE IF NOT EXISTS tenant_nodes (
    node_id VARCHAR(128) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    created_by VARCHAR(32) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    enroll_by DATETIME(6) NOT NULL,
    revoked_at DATETIME(6) NULL,
    revoked_by VARCHAR(32) NOT NULL DEFAULT '',
    INDEX idx_tenant_nodes_tenant_created (tenant_id, created_at)
) ENGINE=InnoDB;
