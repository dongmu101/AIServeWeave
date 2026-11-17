CREATE TABLE IF NOT EXISTS pricing_revisions (
 id BIGSERIAL PRIMARY KEY,
 effective_at TIMESTAMPTZ NOT NULL,
 document TEXT NOT NULL,
 created_by VARCHAR(40) NOT NULL,
 summary VARCHAR(512) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_pricing_revisions_effective ON pricing_revisions (effective_at, id);
CREATE TABLE IF NOT EXISTS tenant_price_plans (
 id VARCHAR(40) PRIMARY KEY,
 tenant_id VARCHAR(40) NOT NULL,
 plan_id VARCHAR(40) NOT NULL,
 valid_from TIMESTAMPTZ NOT NULL,
 valid_to TIMESTAMPTZ NULL,
 created_by VARCHAR(40) NOT NULL
);
CREATE INDEX idx_tenant_price_plans_tenant ON tenant_price_plans (tenant_id, valid_from);
CREATE INDEX idx_tenant_price_plans_plan ON tenant_price_plans (plan_id, valid_to);
CREATE TABLE IF NOT EXISTS pricing_config_state (
 id INTEGER PRIMARY KEY,
 version BIGINT NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
INSERT INTO pricing_config_state (id, version, updated_at) VALUES (1, 0, now()) ON CONFLICT (id) DO NOTHING;
