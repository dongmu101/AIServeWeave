ALTER TABLE response_turns
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, created_at)
 PARTITION BY RANGE COLUMNS(created_at) (/*aisw:partitions table=response_turns*/);
