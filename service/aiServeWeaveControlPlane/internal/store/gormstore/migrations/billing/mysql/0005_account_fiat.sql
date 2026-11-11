ALTER TABLE account_sales ADD COLUMN price_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE account_sales ADD COLUMN currency VARCHAR(8) NOT NULL DEFAULT '';
ALTER TABLE account_sales ADD COLUMN dodo_product_id VARCHAR(64) NOT NULL DEFAULT '';
UPDATE account_sales SET active = FALSE;
ALTER TABLE account_sales DROP COLUMN price_micro;
ALTER TABLE checkout_orders ADD COLUMN account_id VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE checkout_orders ADD COLUMN account_expires_at DATETIME(6) NULL;
CREATE INDEX idx_checkout_orders_account ON checkout_orders (account_id, status, created_at);
