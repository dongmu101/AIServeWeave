CREATE TABLE IF NOT EXISTS tenant_nodes (
    node_id VARCHAR(128) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    created_by VARCHAR(32) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    enroll_by TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ NULL,
    revoked_by VARCHAR(32) NOT NULL DEFAULT ''
);
CREATE INDEX idx_tenant_nodes_tenant_created ON tenant_nodes (tenant_id, created_at);
