UPDATE audit_logs SET created_at = UTC_TIMESTAMP(3) WHERE created_at IS NULL;
ALTER TABLE audit_logs
 MODIFY created_at DATETIME(3) NOT NULL,
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, created_at)
 PARTITION BY RANGE COLUMNS(created_at) (/*aisw:partitions table=audit_logs*/);
