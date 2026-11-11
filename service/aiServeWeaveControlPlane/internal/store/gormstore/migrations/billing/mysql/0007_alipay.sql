CREATE TABLE IF NOT EXISTS alipay_purchases (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 action_id VARCHAR(64) NOT NULL,
 sku_id VARCHAR(128) NOT NULL,
 callback_url VARCHAR(512) NOT NULL,
 status VARCHAR(16) NOT NULL,
 purchase_url VARCHAR(2048) NOT NULL,
 expire_time VARCHAR(64) NOT NULL,
 reason_code VARCHAR(64) NOT NULL,
 created_by VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_alipay_purchases_action ON alipay_purchases (tenant_id, action_id);
CREATE TABLE IF NOT EXISTS alipay_redemptions (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 action_id VARCHAR(64) NOT NULL,
 amount BIGINT NOT NULL,
 quota_unit VARCHAR(8) NOT NULL,
 credits_micro BIGINT NOT NULL,
 reason VARCHAR(256) NOT NULL,
 status VARCHAR(16) NOT NULL,
 reason_code VARCHAR(64) NOT NULL,
 consume_order_id VARCHAR(128) NOT NULL,
 created_by VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_alipay_redemptions_action ON alipay_redemptions (tenant_id, action_id);
CREATE INDEX idx_alipay_redemptions_tenant_created ON alipay_redemptions (tenant_id, created_at, id);
CREATE INDEX idx_alipay_redemptions_status_created ON alipay_redemptions (status, created_at, id);
