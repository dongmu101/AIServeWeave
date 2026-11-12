ALTER TABLE checkout_orders ADD COLUMN provider VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE checkout_orders ADD COLUMN pay_type VARCHAR(16) NOT NULL DEFAULT '';
CREATE INDEX idx_checkout_orders_provider ON checkout_orders (provider, status, created_at);
