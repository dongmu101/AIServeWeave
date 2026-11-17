ALTER TABLE tenant_billing
 ADD COLUMN usage_acc_micro BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN usage_acc_count BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN usage_acc_since DATETIME(6) NULL,
 ADD COLUMN usage_acc_last DATETIME(6) NULL,
 ADD COLUMN usage_acc_until DATETIME(6) NULL,
 ADD COLUMN last_ledger_at DATETIME(6) NULL;
CREATE INDEX idx_tenant_billing_usage_acc_until ON tenant_billing (usage_acc_until);
ALTER TABLE credit_ledger
 ADD COLUMN period_start DATETIME(6) NULL,
 ADD COLUMN period_end DATETIME(6) NULL,
 ADD COLUMN request_count BIGINT NOT NULL DEFAULT 0,
 ALGORITHM=INSTANT;
