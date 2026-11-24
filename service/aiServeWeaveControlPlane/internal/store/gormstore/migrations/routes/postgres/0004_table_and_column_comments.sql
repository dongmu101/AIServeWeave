COMMENT ON TABLE route_revisions IS '模型路由规则的发布版本与回滚历史';
COMMENT ON COLUMN route_revisions.revision IS '路由发布版本号';
COMMENT ON COLUMN route_revisions.digest IS '路由内容摘要';
COMMENT ON COLUMN route_revisions.routes_json IS '完整路由规则快照 JSON';
COMMENT ON COLUMN route_revisions.actor_id IS '发布操作人标识';
COMMENT ON COLUMN route_revisions.created_at IS '创建时间';
COMMENT ON COLUMN route_revisions.rollback_of IS '回滚所引用的历史版本号，0 表示普通发布';

COMMENT ON TABLE route_actives IS '当前生效的模型路由版本指针';
COMMENT ON COLUMN route_actives.id IS '单行记录标识，固定为 1';
COMMENT ON COLUMN route_actives.revision IS '当前生效的路由版本号，0 表示未发布';

COMMENT ON TABLE schema_migrations_routes IS '模型路由迁移的版本账本';
COMMENT ON COLUMN schema_migrations_routes.id IS '迁移 SQL 文件名';
COMMENT ON COLUMN schema_migrations_routes.applied_at IS '迁移完成时间，未完成时记录本次尝试的开始时间';
COMMENT ON COLUMN schema_migrations_routes.checksum IS '迁移 SQL 的 SHA-256 校验和';
COMMENT ON COLUMN schema_migrations_routes.dirty IS '迁移是否仍未完成';
