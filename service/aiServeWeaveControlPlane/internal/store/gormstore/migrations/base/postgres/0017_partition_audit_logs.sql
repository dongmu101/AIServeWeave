DROP INDEX IF EXISTS idx_audit_logs_tenant_id;
DROP INDEX IF EXISTS idx_audit_logs_actor_id;
DROP INDEX IF EXISTS idx_audit_logs_action;
DROP INDEX IF EXISTS idx_audit_logs_created_at;
DROP INDEX IF EXISTS idx_audit_logs_tenant_created;
ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS audit_logs_pkey;
ALTER TABLE audit_logs RENAME TO audit_logs_unpartitioned;
CREATE TABLE audit_logs (
    id VARCHAR(32) NOT NULL,
    tenant_id VARCHAR(32) NOT NULL,
    actor_id VARCHAR(32),
    action VARCHAR(64) NOT NULL,
    target VARCHAR(64),
    detail VARCHAR(512),
    ip VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
UPDATE audit_logs_unpartitioned SET created_at = now() WHERE created_at IS NULL;
/*aisw:partitions table=audit_logs source=audit_logs_unpartitioned*/;
INSERT INTO audit_logs (id, tenant_id, actor_id, action, target, detail, ip, created_at)
 SELECT id, tenant_id, actor_id, action, target, detail, ip, created_at FROM audit_logs_unpartitioned;
DROP TABLE audit_logs_unpartitioned;
CREATE INDEX idx_audit_logs_tenant_id ON audit_logs (tenant_id);
CREATE INDEX idx_audit_logs_actor_id ON audit_logs (actor_id);
CREATE INDEX idx_audit_logs_action ON audit_logs (action);
CREATE INDEX idx_audit_logs_created_at ON audit_logs (created_at);
CREATE INDEX idx_audit_logs_tenant_created ON audit_logs (tenant_id, created_at, id);
