ALTER TABLE checkout_orders ADD COLUMN paid_amount_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE checkout_orders ADD COLUMN paid_currency VARCHAR(8) NOT NULL DEFAULT '';
