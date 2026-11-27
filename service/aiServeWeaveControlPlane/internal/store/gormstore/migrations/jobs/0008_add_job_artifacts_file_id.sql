ALTER TABLE job_artifacts
    ADD COLUMN file_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '所存字节对应的文件标识，只有元数据时为空';
