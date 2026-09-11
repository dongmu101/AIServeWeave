CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(32) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    email VARCHAR(255) NOT NULL,
    password_hash VARCHAR(120) NOT NULL,
    name VARCHAR(128),
    role VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL,
    created_at DATETIME(3),
    updated_at DATETIME(3)
) ENGINE=InnoDB;
