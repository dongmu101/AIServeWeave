COMMENT ON TABLE workflow_template_revisions IS '工作流模板的发布版本与回滚历史';
COMMENT ON COLUMN workflow_template_revisions.template_id IS '工作流模板标识';
COMMENT ON COLUMN workflow_template_revisions.revision IS '模板发布版本号';
COMMENT ON COLUMN workflow_template_revisions.digest IS '模板内容摘要';
COMMENT ON COLUMN workflow_template_revisions.description IS '模板说明';
COMMENT ON COLUMN workflow_template_revisions.inputs_json IS '工作流输入参数定义 JSON';
COMMENT ON COLUMN workflow_template_revisions.outputs_json IS '工作流输出定义 JSON';
COMMENT ON COLUMN workflow_template_revisions.dependencies_json IS '工作流依赖清单 JSON';
COMMENT ON COLUMN workflow_template_revisions.visible_tenants_json IS '可见租户标识列表 JSON';
COMMENT ON COLUMN workflow_template_revisions.graph_json IS '提交给后端执行的工作流图 JSON';
COMMENT ON COLUMN workflow_template_revisions.actor_id IS '发布操作人标识';
COMMENT ON COLUMN workflow_template_revisions.created_at IS '创建时间';
COMMENT ON COLUMN workflow_template_revisions.rollback_of IS '回滚所引用的历史版本号，0 表示普通发布';
COMMENT ON COLUMN workflow_template_revisions.workflow_json IS '供工作流编辑器使用的界面工作流 JSON';

COMMENT ON TABLE workflow_template_actives IS '每个工作流模板当前生效的版本指针';
COMMENT ON COLUMN workflow_template_actives.template_id IS '工作流模板标识';
COMMENT ON COLUMN workflow_template_actives.revision IS '当前生效的模板版本号，0 表示未发布';

COMMENT ON TABLE schema_migrations_workflow_templates IS '工作流模板迁移的版本账本';
COMMENT ON COLUMN schema_migrations_workflow_templates.id IS '迁移 SQL 文件名';
COMMENT ON COLUMN schema_migrations_workflow_templates.applied_at IS '迁移完成时间，未完成时记录本次尝试的开始时间';
COMMENT ON COLUMN schema_migrations_workflow_templates.checksum IS '迁移 SQL 的 SHA-256 校验和';
COMMENT ON COLUMN schema_migrations_workflow_templates.dirty IS '迁移是否仍未完成';
