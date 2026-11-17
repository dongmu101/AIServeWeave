CREATE TABLE IF NOT EXISTS pricing_revisions (
 id BIGINT AUTO_INCREMENT PRIMARY KEY,
 effective_at DATETIME(6) NOT NULL,
 document MEDIUMTEXT NOT NULL,
 created_by VARCHAR(40) NOT NULL,
 summary VARCHAR(512) NOT NULL,
 created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE INDEX idx_pricing_revisions_effective ON pricing_revisions (effective_at, id);
CREATE TABLE IF NOT EXISTS tenant_price_plans (
 id VARCHAR(40) PRIMARY KEY,
 tenant_id VARCHAR(40) NOT NULL,
 plan_id VARCHAR(40) NOT NULL,
 valid_from DATETIME(6) NOT NULL,
 valid_to DATETIME(6) NULL,
 created_by VARCHAR(40) NOT NULL
) ENGINE=InnoDB;
CREATE INDEX idx_tenant_price_plans_tenant ON tenant_price_plans (tenant_id, valid_from);
CREATE INDEX idx_tenant_price_plans_plan ON tenant_price_plans (plan_id, valid_to);
CREATE TABLE IF NOT EXISTS pricing_config_state (
 id INT PRIMARY KEY,
 version BIGINT NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
INSERT IGNORE INTO pricing_config_state (id, version, updated_at) VALUES (1, 0, UTC_TIMESTAMP(6));
