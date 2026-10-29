CREATE TABLE tenant_billing (
 tenant_id VARCHAR(32) PRIMARY KEY,
 mode VARCHAR(16) NOT NULL,
 balance_micro BIGINT NOT NULL,
 dodo_customer_id VARCHAR(64) NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE credit_packages (
 id VARCHAR(32) PRIMARY KEY,
 name VARCHAR(128) NOT NULL,
 kind VARCHAR(16) NOT NULL,
 dodo_product_id VARCHAR(64) NOT NULL,
 credits_micro BIGINT NOT NULL,
 price_minor BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL,
 credits_per_minor_unit_micro BIGINT NOT NULL,
 min_amount_minor BIGINT NOT NULL,
 max_amount_minor BIGINT NOT NULL,
 active BOOLEAN NOT NULL,
 sort_order BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE model_prices (
 model VARCHAR(128) PRIMARY KEY,
 input_micro_per_m_tok BIGINT NOT NULL,
 output_micro_per_m_tok BIGINT NOT NULL,
 updated_by VARCHAR(32) NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE checkout_orders (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 package_id VARCHAR(32) NOT NULL,
 dodo_session_id VARCHAR(128) NOT NULL,
 dodo_payment_id VARCHAR(64),
 amount_minor BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL,
 credits_micro BIGINT NOT NULL,
 refunded_minor BIGINT NOT NULL,
 status VARCHAR(16) NOT NULL,
 created_by VARCHAR(32) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_checkout_orders_payment ON checkout_orders (dodo_payment_id);
CREATE INDEX idx_checkout_orders_tenant_created ON checkout_orders (tenant_id, created_at, id);
CREATE INDEX idx_checkout_orders_created ON checkout_orders (created_at, id);
CREATE TABLE credit_ledger (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 kind VARCHAR(24) NOT NULL,
 amount_micro BIGINT NOT NULL,
 balance_after_micro BIGINT NOT NULL,
 ref VARCHAR(128) NOT NULL,
 idempotency_key VARCHAR(160),
 actor_id VARCHAR(32) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_credit_ledger_idempotency ON credit_ledger (idempotency_key);
CREATE INDEX idx_credit_ledger_tenant_created ON credit_ledger (tenant_id, created_at, id);
CREATE INDEX idx_credit_ledger_created ON credit_ledger (created_at, id);
CREATE TABLE usage_charges (
 request_id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 model VARCHAR(128) NOT NULL,
 cost_micro BIGINT NOT NULL,
 charged BOOLEAN NOT NULL,
 created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_usage_charges_tenant_created ON usage_charges (tenant_id, created_at);
CREATE TABLE billing_webhook_events (
 webhook_id VARCHAR(128) PRIMARY KEY,
 type VARCHAR(64) NOT NULL,
 received_at TIMESTAMPTZ NOT NULL
);
