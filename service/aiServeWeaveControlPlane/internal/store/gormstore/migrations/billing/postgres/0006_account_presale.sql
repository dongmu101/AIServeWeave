CREATE TABLE IF NOT EXISTS account_presales (
 id VARCHAR(32) PRIMARY KEY,
 kind VARCHAR(16) NOT NULL,
 name VARCHAR(128) NOT NULL,
 price_minor BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL,
 dodo_product_id VARCHAR(64) NOT NULL,
 capacity BIGINT NOT NULL,
 active BOOLEAN NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE account_sales ADD COLUMN kind VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE checkout_orders ADD COLUMN presale_id VARCHAR(32) NOT NULL DEFAULT '';
CREATE INDEX idx_checkout_orders_presale ON checkout_orders (presale_id, status, created_at);
