# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/) — with one caveat while it stays on
`0.x`: **minor version bumps may still contain breaking changes** (protocol,
config, database schema, CLI flags). Cross-version rolling upgrades and
protocol/config compatibility are not yet a supported goal — see the `A03`
entry in [STATUS.md](STATUS.md). Read a release's own entry for what changed
before upgrading; do not assume a `0.x` bump is safe by default.

本文件记录所有值得关注的变更。格式遵循 [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)，
版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)——但在 `0.x` 阶段有一条例外：
**minor 版本升级仍可能包含破坏性变更**（协议、配置、数据库 schema、CLI flag）。跨版本
滚动升级与协议/配置兼容性尚未作为已支持的目标，见 [STATUS.md](STATUS.md) 的 `A03` 条目。
升级前请读清楚目标版本自己的条目，不要默认 `0.x` 的版本升级是安全的。

## [Unreleased]

### Added

- P07 数据库升级与恢复：双引擎固定版本 SQL、锁与校验和/dirty 账本、独立 up/status/resume 命令、已有数据升级及原生备份恢复测试；事务 outbox 跨崩溃补发 Key 吊销。
- P05 用户与会话生命周期：Redis 权威可吊销会话、租户用户与平台运维改密/重置/禁用/启用/角色及会话管理，以及对应 Console 页面。
- P06 Key 吊销通知：ControlPlane 以 Redis generation 长轮询唤醒 Gateway；健康链路 1 秒内清缓存，通知异常时 Gateway 立即停用正向缓存并逐请求校验。

### Changed

- Redis 成为 ControlPlane 必需依赖，Compose 使用 AOF `appendfsync always`；无 `sid` 的旧 JWT 升级后失效。
- 禁用租户用户会在同一数据库事务中吊销其创建的 active API Key；ControlPlane Key 缓存改用 generation 防止并发旧读取回填。
- Gateway 的 `-key-cache-ttl` 保留为纵深防御；提交后通知前崩溃由 P07 outbox 重试，漏通知、断线重连和控制面副本切换由持久 generation 补偿。

## [0.1.0] - 2026-09-07

首个可安装、可部署的版本：三个数据面/身份服务加 Console 均有版本化二进制与容器镜像；
适用范围以 [STATUS.md](STATUS.md) 当前已完成条目为准，以下是这份发布可用能力的摘要，
不是完整清单。

First installable, deployable release: the three data-plane/identity services
and the Console all get versioned binaries and container images. The scope
below summarizes what this release can do; [STATUS.md](STATUS.md)'s current
"已完成" section is authoritative, not this list.

### Added

- Registry：节点证书签发与续期、`GatewayDirectory` 副本名册、`node_id` 唯一性检测
  （S01）、受控注册令牌管理（S02）、Gateway 加入认证与节点禁用/证书吊销（S03）。
- Agent：主动出站 mTLS 隧道、多副本连接、槽池；Ollama/vLLM/SGLang/ComfyUI 运行时适配；
  本机 Ollama/vLLM 自动发现。
- Gateway：OpenAI 兼容的 Chat Completions/Responses/Embeddings/Models 前门（含流式）；
  模型别名、标签、优先级与权重路由，健康过滤、熔断与受限重试；工作流模板加载、Job
  提交/状态/SSE/取消/产物；租户请求数、token 数与并发限流（内存与 Redis 两种实现）；
  受持久化保护的 Job 可靠性闭环（重启恢复、跨副本访问，详见控制面「Job 持久化契约」）。
- 控制面：租户/用户/API Key/审计的 Admin API（go-zero + gorm + Redis），配额读写，
  游标分页与服务端筛选；持久化 Job 历史查询（`/admin/v1/jobs/history*`）。
- Console：登录与服务端会话、用户/Key/审计/配额页面、只读节点与模型清单、工作流目录
  与实时 Job 视图、Job 历史与取消/产物预览。
- Agent/Gateway Prometheus 指标导出；`deploy/docker-compose.yaml` 部署配置（含 Console）。
- 开源治理文件（LICENSE：Apache-2.0、CONTRIBUTING.md、SECURITY.md）与 GitHub Actions
  CI（Go 质量门禁、Console 门禁、真实 MySQL 9.7 集成测试）。
- 版本化二进制与容器镜像、校验和、发布流水线（本条目本身，R03）。

### Known limitations

- Job 的权威路由绑定以单个 Gateway 副本的有界内存为主，一个还没来得及持久化就被逐出的
  job 仍会永久丢失；详见 [STATUS.md](STATUS.md#当前主要边界)。
- 节点、模型、工作流模板的管理写路径（P01–P03）与产物持久存储（P04）尚未交付，只有
  只读清单。
- 跨版本滚动升级与配置/协议兼容性未验证（A03）；本版本假定全新部署或整体停机升级。

[Unreleased]: https://github.com/dongmu101/AIServeWeave/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/dongmu101/AIServeWeave/releases/tag/v0.1.0
