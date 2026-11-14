ALTER TABLE usage_records
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, created_at)
 PARTITION BY RANGE COLUMNS(created_at) (/*aisw:partitions table=usage_records*/);
