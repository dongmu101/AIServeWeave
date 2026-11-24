ALTER TABLE workflow_template_revisions
    MODIFY COLUMN template_id VARCHAR(128) NOT NULL COMMENT '工作流模板标识',
    MODIFY COLUMN revision BIGINT NOT NULL COMMENT '模板发布版本号',
    MODIFY COLUMN digest VARCHAR(64) NOT NULL COMMENT '模板内容摘要',
    MODIFY COLUMN description TEXT NOT NULL COMMENT '模板说明',
    MODIFY COLUMN inputs_json MEDIUMTEXT NOT NULL COMMENT '工作流输入参数定义 JSON',
    MODIFY COLUMN outputs_json MEDIUMTEXT NOT NULL COMMENT '工作流输出定义 JSON',
    MODIFY COLUMN dependencies_json MEDIUMTEXT NOT NULL COMMENT '工作流依赖清单 JSON',
    MODIFY COLUMN visible_tenants_json MEDIUMTEXT NOT NULL COMMENT '可见租户标识列表 JSON',
    MODIFY COLUMN graph_json MEDIUMTEXT NOT NULL COMMENT '提交给后端执行的工作流图 JSON',
    MODIFY COLUMN actor_id VARCHAR(40) NOT NULL COMMENT '发布操作人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN rollback_of BIGINT NOT NULL DEFAULT 0 COMMENT '回滚所引用的历史版本号，0 表示普通发布',
    MODIFY COLUMN workflow_json MEDIUMTEXT NOT NULL DEFAULT ('') COMMENT '供工作流编辑器使用的界面工作流 JSON',
    COMMENT = '工作流模板的发布版本与回滚历史', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE workflow_template_actives
    MODIFY COLUMN template_id VARCHAR(128) NOT NULL COMMENT '工作流模板标识',
    MODIFY COLUMN revision BIGINT NOT NULL DEFAULT 0 COMMENT '当前生效的模板版本号，0 表示未发布',
    COMMENT = '每个工作流模板当前生效的版本指针', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_workflow_templates
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '工作流模板迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;
