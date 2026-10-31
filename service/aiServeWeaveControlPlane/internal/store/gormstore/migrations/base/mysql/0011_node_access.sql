CREATE TABLE IF NOT EXISTS node_access (
    node_id VARCHAR(128) PRIMARY KEY,
    tenants_json MEDIUMTEXT NOT NULL,
    updated_by VARCHAR(32) NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
