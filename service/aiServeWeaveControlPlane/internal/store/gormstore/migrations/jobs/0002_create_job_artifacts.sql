CREATE TABLE job_artifacts (
    id         VARCHAR(64)  NOT NULL,
    job_id     VARCHAR(64)  NOT NULL,
    tenant_id  VARCHAR(32)  NOT NULL,
    filename   VARCHAR(512) NOT NULL,
    subfolder  VARCHAR(512) NOT NULL DEFAULT '',
    type       VARCHAR(32)  NOT NULL DEFAULT '',
    created_at DATETIME(6)  NOT NULL,
    PRIMARY KEY (id),
    KEY idx_job_artifacts_job (job_id),
    KEY idx_job_artifacts_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
