CREATE TABLE IF NOT EXISTS node_access (
    node_id VARCHAR(128) PRIMARY KEY,
    tenants_json TEXT NOT NULL,
    updated_by VARCHAR(32) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
