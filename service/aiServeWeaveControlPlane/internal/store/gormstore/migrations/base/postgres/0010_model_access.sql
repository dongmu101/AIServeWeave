CREATE TABLE IF NOT EXISTS model_access (
    scope VARCHAR(64) PRIMARY KEY,
    models_json TEXT NOT NULL,
    updated_by VARCHAR(32) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
