ALTER TABLE job_artifacts ADD INDEX idx_job_artifacts_type_created (type, created_at, id);
