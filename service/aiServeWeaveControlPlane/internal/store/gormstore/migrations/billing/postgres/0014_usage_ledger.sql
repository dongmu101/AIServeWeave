ALTER TABLE tenant_billing
 ADD COLUMN usage_acc_micro BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN usage_acc_count BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN usage_acc_since TIMESTAMPTZ NULL,
 ADD COLUMN usage_acc_last TIMESTAMPTZ NULL,
 ADD COLUMN usage_acc_until TIMESTAMPTZ NULL,
 ADD COLUMN last_ledger_at TIMESTAMPTZ NULL;
CREATE INDEX idx_tenant_billing_usage_acc_until ON tenant_billing (usage_acc_until);
ALTER TABLE credit_ledger
 ADD COLUMN period_start TIMESTAMPTZ NULL,
 ADD COLUMN period_end TIMESTAMPTZ NULL,
 ADD COLUMN request_count BIGINT NOT NULL DEFAULT 0;
