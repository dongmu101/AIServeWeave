ALTER TABLE jobs ADD INDEX idx_jobs_node_runtime (node_id, runtime_id, state);
