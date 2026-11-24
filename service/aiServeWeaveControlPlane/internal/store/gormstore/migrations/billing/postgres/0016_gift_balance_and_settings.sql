ALTER TABLE tenant_billing
 ADD COLUMN gift_balance_micro BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN usage_acc_gift_micro BIGINT NOT NULL DEFAULT 0;
ALTER TABLE credit_ledger
 ADD COLUMN gift_amount_micro BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN gift_balance_after_micro BIGINT NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS billing_settings (
 id INTEGER PRIMARY KEY,
 signup_grant_micro BIGINT NOT NULL,
 updated_by VARCHAR(40) NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
COMMENT ON COLUMN tenant_billing.gift_balance_micro IS '当前余额中赠送点数的部分，单位为微点';
COMMENT ON COLUMN tenant_billing.usage_acc_gift_micro IS '待封账用量中从赠送点数扣除的部分，单位为微点';
COMMENT ON COLUMN credit_ledger.gift_amount_micro IS '本次变动中赠送点数的部分，单位为微点';
COMMENT ON COLUMN credit_ledger.gift_balance_after_micro IS '变动后余额中赠送点数的部分，单位为微点';
COMMENT ON TABLE billing_settings IS '运维在后台保存的计费设置';
COMMENT ON COLUMN billing_settings.id IS '单行记录标识，固定为 1';
COMMENT ON COLUMN billing_settings.signup_grant_micro IS '自助注册租户获赠的点数，单位为微点';
COMMENT ON COLUMN billing_settings.updated_by IS '最后修改的运维人员标识';
COMMENT ON COLUMN billing_settings.updated_at IS '最后更新时间';
