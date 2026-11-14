ALTER TABLE request_logs
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, created_at)
 PARTITION BY RANGE COLUMNS(created_at) (/*aisw:partitions table=request_logs*/);
