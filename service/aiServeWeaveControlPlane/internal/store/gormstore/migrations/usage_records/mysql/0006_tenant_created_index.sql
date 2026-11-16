ALTER TABLE usage_records ADD INDEX idx_usage_records_tenant_created (tenant_id, created_at, id), ALGORITHM=INPLACE, LOCK=NONE;
