CREATE TABLE account_sales_lock (id BIGINT PRIMARY KEY);
INSERT INTO account_sales_lock (id) VALUES (1);
CREATE TABLE account_sales (
 id VARCHAR(32) PRIMARY KEY,
 node_id VARCHAR(128) NOT NULL,
 runtime_id VARCHAR(128) NOT NULL,
 name VARCHAR(128) NOT NULL,
 price_micro BIGINT NOT NULL,
 active BOOLEAN NOT NULL,
 tenant_id VARCHAR(32) NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_account_sales_target ON account_sales (node_id, runtime_id);
CREATE TABLE account_purchases (
 id VARCHAR(32) PRIMARY KEY,
 account_id VARCHAR(32) NOT NULL,
 tenant_id VARCHAR(32) NOT NULL,
 price_micro BIGINT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL
);
