CREATE TABLE IF NOT EXISTS audit_logs (
    id VARCHAR(32) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    actor_id VARCHAR(32),
    action VARCHAR(64) NOT NULL,
    target VARCHAR(64),
    detail VARCHAR(512),
    ip VARCHAR(64),
    created_at TIMESTAMPTZ
);
