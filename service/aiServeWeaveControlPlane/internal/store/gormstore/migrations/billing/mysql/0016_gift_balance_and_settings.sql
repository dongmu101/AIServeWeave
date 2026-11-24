ALTER TABLE tenant_billing
 ADD COLUMN gift_balance_micro BIGINT NOT NULL DEFAULT 0 COMMENT '当前余额中赠送点数的部分，单位为微点',
 ADD COLUMN usage_acc_gift_micro BIGINT NOT NULL DEFAULT 0 COMMENT '待封账用量中从赠送点数扣除的部分，单位为微点',
 ALGORITHM=INSTANT;
ALTER TABLE credit_ledger
 ADD COLUMN gift_amount_micro BIGINT NOT NULL DEFAULT 0 COMMENT '本次变动中赠送点数的部分，单位为微点',
 ADD COLUMN gift_balance_after_micro BIGINT NOT NULL DEFAULT 0 COMMENT '变动后余额中赠送点数的部分，单位为微点',
 ALGORITHM=INSTANT;
CREATE TABLE IF NOT EXISTS billing_settings (
 id INT NOT NULL COMMENT '单行记录标识，固定为 1',
 signup_grant_micro BIGINT NOT NULL COMMENT '自助注册租户获赠的点数，单位为微点',
 updated_by VARCHAR(40) NOT NULL COMMENT '最后修改的运维人员标识',
 updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
 PRIMARY KEY (id)
) ENGINE=InnoDB COMMENT='运维在后台保存的计费设置';
