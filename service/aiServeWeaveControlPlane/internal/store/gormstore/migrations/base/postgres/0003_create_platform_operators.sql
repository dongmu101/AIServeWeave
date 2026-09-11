CREATE TABLE IF NOT EXISTS platform_operators (
    id VARCHAR(32) PRIMARY KEY,
    email VARCHAR(255) NOT NULL,
    password_hash VARCHAR(120) NOT NULL,
    name VARCHAR(128),
    status VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ
);
