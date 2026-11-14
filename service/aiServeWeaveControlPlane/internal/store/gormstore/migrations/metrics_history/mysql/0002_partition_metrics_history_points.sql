ALTER TABLE metrics_history_points
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, bucket_at)
 PARTITION BY RANGE COLUMNS(bucket_at) (/*aisw:partitions table=metrics_history_points*/);
