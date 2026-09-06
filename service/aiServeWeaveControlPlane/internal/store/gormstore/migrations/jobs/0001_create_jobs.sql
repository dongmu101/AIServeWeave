CREATE TABLE jobs (
    id               VARCHAR(64)   NOT NULL,
    tenant_id        VARCHAR(32)   NOT NULL,
    workflow_id      VARCHAR(128)  NOT NULL,
    workflow_version VARCHAR(64)   NOT NULL DEFAULT '',
    node_id          VARCHAR(128)  NOT NULL,
    runtime_id       VARCHAR(128)  NOT NULL,
    backend_run_id   VARCHAR(256)  NOT NULL,
    state            VARCHAR(16)   NOT NULL,
    error_summary    VARCHAR(512)  NOT NULL DEFAULT '',
    observed_seq     BIGINT        NOT NULL DEFAULT 0,
    created_at       DATETIME(6)   NOT NULL,
    updated_at       DATETIME(6)   NOT NULL,
    terminal_at      DATETIME(6)   NULL,
    PRIMARY KEY (id),
    KEY idx_jobs_tenant_created (tenant_id, created_at, id),
    KEY idx_jobs_tenant_status (tenant_id, state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
