# 数据模型

关系数据库由 ControlPlane 的 `Database.Driver` 选择。Job 持久化以 MySQL 9.7 / InnoDB 为目标，保留既有 PostgreSQL 接入；跨引擎兼容范围以迁移与集成测试为准，不假定 JSON 类型、索引或锁行为相同。Gateway 与 Agent 不直接依赖关系数据库。

以下为逻辑数据模型，表示实体职责与关系，不要求每个条目都独立建表。物理表、索引与迁移随相应能力设计；具体完成状态见 [STATUS.md](../STATUS.md)。

```text
tenants
users
api_keys

nodes
node_credentials
node_heartbeats
node_labels

backends
models
deployments
deployment_capabilities
deployment_revisions
deployment_status

routes
route_targets

workflow_templates
workflow_versions
workflow_requirements
jobs
job_events
job_artifacts
artifacts

inference_requests
usage_records
audit_logs
```

主要关系：

- 一个 Node 可以运行多个 Backend
- 一个 Backend 可以运行多个 Deployment
- Managed Deployment 使用 Revision 保存声明式配置，并分别记录期望状态和实际状态
- 一个 Model 可以通过 Route 指向多个 Deployment
- Route Target 保存权重、优先级和匹配条件
- 一个 Workflow Template 可以有多个不可变版本
- Job 保存公开任务 ID、ComfyUI `prompt_id` 和实际 Deployment 的映射
- Artifact 保存输入文件、预览图和最终生成文件的元数据；Job Artifact 保存任务与稳定公开产物 ID 的关联

任务、事件、文件元数据与请求摘要均需定义保留期和有界清理。Prometheus 承担指标采集，历史指标与日志使用适合查询规模的存储；用量账本独立定义去重与结算规则。备份范围应同时覆盖数据库、Registry 身份材料和对象存储，恢复后进行引用一致性检查。

schema 升级、迁移失败处理与备份恢复的操作步骤见 [数据库升级与恢复](../deploy/database-recovery.md)。
