CREATE INDEX idx_users_tenant_created ON users (tenant_id, created_at, id);
CREATE INDEX idx_api_keys_tenant_created ON api_keys (tenant_id, created_at, id);
CREATE INDEX idx_audit_logs_tenant_created ON audit_logs (tenant_id, created_at, id);
