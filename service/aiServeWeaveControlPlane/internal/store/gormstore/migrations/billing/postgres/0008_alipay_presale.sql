ALTER TABLE account_presales ADD COLUMN alipay_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE checkout_orders ADD COLUMN alipay_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE alipay_redemptions ADD COLUMN presale_order_id VARCHAR(32) NOT NULL DEFAULT '';
