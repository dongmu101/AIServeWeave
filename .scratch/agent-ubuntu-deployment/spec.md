# Ubuntu Agent 独立部署规格

Status: ready-for-agent

更新日期：2026-10-30。依据本功能部署方案中已确认的 Q1–Q13 整理；状态表示规格可供实施，安装能力交付和实机验收结果另行记录。

## Problem Statement

节点管理员需要把 Agent 独立安装到 Ubuntu 推理节点，连接已有 Registry 与 Gateway，并长期使用本机 HTTP/API 后端以及已登录的 Codex、Claude CLI。

当前部署示例主要覆盖源码运行，尚缺 Agent 专用的安装、开机启动、服务账户配置和维护闭环。仅有 Linux 二进制构建配置不足以证明目标 Ubuntu 版本可以正确注册、续期、使用 CLI 或在升级后保持选定版本。配置工作目录、身份目录权限及后台账户环境的差异，还可能造成进程存活而节点或后端不可用。

## Solution

为 Ubuntu 提供预编译 Agent 二进制、系统级 systemd 集成及配套安装维护流程。管理员使用 sudo 安装，常驻进程由明确指定的普通用户运行；支持指定版本的在线下载与本地文件安装，无需在目标机安装 Go 或平台其他服务。

覆盖 Ubuntu 22.04、24.04、26.04 LTS 的 amd64 与 arm64。沿用现有平台协议、节点身份、运行时声明和 CLI 账户边界，提供首次接入、开机启动、回环设置页、显式升级、手动回退与保留状态的卸载流程。各组合通过真实系统验收后才标记为已验证。

## User Stories

1. As a node administrator, I want to install only Agent on an Ubuntu node, so that the node can join my existing Registry and Gateway deployment.
2. As a node administrator, I want a precompiled binary, so that the target node does not need Go or a source checkout.
3. As a node administrator, I want the supported Ubuntu versions and CPU architectures stated explicitly, so that I can choose an appropriate artifact and validation target.
4. As a node administrator, I want to select an exact release, so that installations are reproducible and do not change with the latest release.
5. As a node administrator, I want online installation, so that I can obtain the selected release on a connected node.
6. As a node administrator, I want local-file installation, so that I can deploy on a node without downloading artifacts there.
7. As a node administrator, I want artifact checksums and architecture checked before activation, so that a damaged or incorrect binary never replaces a working installation.
8. As a node administrator, I want actionable preflight errors, so that unsupported systems, missing users and permission problems are visible before activation.
9. As a node administrator, I want to install the system service with sudo while running Agent as a named ordinary user, so that routine inference does not require root.
10. As a node administrator, I want Agent to start after a machine reboot without an interactive login, so that the node can operate unattended.
11. As a CLI account owner, I want Agent to use my intended system account, environment and account directory, so that the CLI can reuse its own login.
12. As a CLI account owner, I want existing multi-account declarations preserved, so that each runtime continues to use its intended subscription account.
13. As a CLI account owner, I want the installer to leave CLI programs and credentials under my control, so that deployment does not copy or migrate my login.
14. As a platform operator, I want to provide a unique node ID, bound one-time token, CA and reachable endpoints, so that node enrollment follows the existing authorization process.
15. As a platform operator, I want remote endpoint and certificate-name requirements documented, so that a same-machine example is not mistaken for a remote deployment configuration.
16. As a node administrator, I want identity material protected and its parent directories writable by the service account, so that registration and renewal can succeed safely.
17. As a node administrator, I want successful registration to consume the one-time token and preserve the node identity, so that subsequent starts reuse the registered node.
18. As a node administrator, I want certificate renewal verified under the actual service account, so that a read-only identity directory does not cause a later outage.
19. As a node administrator, I want repeat installation to preserve configuration and identity, so that maintenance does not create a different node or lose backend declarations.
20. As a platform operator, I want service state, tunnel connectivity, runtime health and model schedulability distinguished, so that a running process is not reported as a working inference node.
21. As an API client, I want ordinary and streamed requests to traverse Gateway and Agent, so that both response modes work through the installed service.
22. As a node administrator, I want the existing local runtime allowlist enforced, so that deployment does not broaden what Gateway can execute.
23. As a node administrator, I want an unavailable ordinary backend to remain diagnosable without hiding other healthy runtimes, so that backend readiness and Agent readiness remain separate.
24. As a node administrator, I want to edit YAML and use the existing local settings page through SSH forwarding, so that I can configure a remote node without a public management listener.
25. As a node administrator, I want runtime edits to reload while connection and identity changes clearly require a restart, so that configuration operations match existing behavior.
26. As a node administrator, I want normal service shutdown to clean up CLI descendants, so that stopped or restarted nodes do not leave inference processes running.
27. As a node administrator, I want bounded and compatible shutdown behavior, so that the service manager can stop Agent predictably.
28. As a node administrator, I want explicit upgrades during a maintenance window, so that a release change is controlled and observable.
29. As a node administrator, I want download or checksum failures to leave the running version intact, so that a failed upgrade does not interrupt service.
30. As a node administrator, I want an explicit rollback to a retained compatible version, so that I can recover without overwriting the current node identity.
31. As a node administrator, I want the selected version to survive service and machine restarts, so that a later start does not silently revert the release.
32. As a node administrator, I want uninstall to retain configuration and identity by default, so that reinstallation can reuse the node.
33. As a CLI account owner, I want Agent cleanup to leave my CLI account directories untouched, so that uninstalling Agent does not log me out or delete other work.
34. As a platform operator, I want secrets and full inference inputs excluded from logs and errors, so that deployment diagnostics preserve the existing confidentiality boundary.
35. As a maintainer, I want build, default-test and real-environment evidence reported separately for every target combination, so that compatibility claims are supported by the checks actually performed.
36. As a maintainer, I want existing macOS and manual-start behavior preserved, so that Ubuntu service support does not regress other deployments.

## Implementation Decisions

1. **平台边界**：仅安装 Agent，接入已有 Registry 与 Gateway。首次接入与验收使用同一发布版本的平台组件；不承诺跨 0.x minor 互通。Agent 主动出站，设置页和指标沿用回环监听，无需新增公网推理或管理入口。
2. **平台矩阵**：目标为 Ubuntu 22.04、24.04、26.04 LTS，各覆盖 amd64、arm64。构建遵守仓库声明的 Go 工具链，关闭 CGO，使用通用 CPU 指令基线；编译完整 Agent 入口包。
3. **职责划分**：部署集成负责制品选择、校验、安装、服务注册和维护；Agent 继续负责本地配置、节点身份、运行时管理与隧道。复用现有配置和身份语义，不为安装器另造一套节点或运行时契约，不要求数据库变更。
4. **安装维护接口**：提供安装、状态检查、启动、停止、运行时重载、指定版本升级、手动回退和卸载操作。支持在线与本地文件来源，均固定版本并验证所选制品摘要；未下载的其他平台或服务制品不影响该制品校验。
5. **预检与失败结果**：检查系统版本、原生 CPU 架构、systemd、指定普通用户及其家目录、配置与状态目录权限、制品类型和必要工具。校验失败时返回可诊断结果，不激活错误制品；本地文件安装不要求下载制品或编译源码。
6. **账户和存储**：管理员维护版本化二进制、稳定启动入口、服务定义和权限受限的服务环境；服务账户可写本地 YAML、节点身份及所需运行状态。具体布局沿用已确认部署方案。服务使用绝对配置位置、明确工作目录、HOME 和 PATH，不依赖交互 shell。
7. **CLI 登录**：Agent 与 CLI 使用同一系统账户，沿用本地 account_dir 和相应 CLI 账户环境。由 CLI 自行管理登录；安装器不读取、复制或迁移凭据，不自动安装或升级 CLI。后台权限限制必须允许已选择的 CLI 程序、账户目录和临时目录正常使用。
8. **首次接入**：平台管理员提供唯一节点 ID、绑定该节点的一次性令牌、CA 和可达端点。令牌通过受限文件导入，不进入明文参数或日志，不持久化平台管理员凭据。注册后沿用既有令牌消费、证书重载和续期规则。
9. **身份权限**：节点证书和私钥符合现有 0600 要求，绑定令牌不允许其他用户读取。身份父目录必须支持服务账户完成创建、临时写入、替换与续期；不能只保证文件可读。重复安装保留既有身份，重新导入令牌须为显式操作。
10. **systemd 行为**：使用系统级服务，由普通用户前台运行 Agent，支持开机启动。模板以最旧目标系统的 systemd 能力为基线，保留现有退出和 SIGHUP 行为。停止预算应与实际清理匹配，并验证 CLI 后代回收；不能把传入清理上下文视为停止期限已生效的证明。
11. **设置与可见状态**：保留 YAML 和回环设置页，通过 SSH 转发访问。页面保存与 SIGHUP 仍仅重载运行时声明；连接、身份、允许列表等既有需重启配置继续需重启。状态检查分别展示或引导验证进程、隧道、运行时、模型与请求结果，避免首次仅配置模式被误判为接入成功。
12. **后端范围**：保留现有 Agent 能力，优先验收 HTTP/托管 API 后端与 Codex、Claude。其余 CLI 和 Managed 后端分别记录条件与验证状态。Agent 本体不统一要求 Docker、GPU、CUDA 或模型安装；显式启用 Managed 后端后的失败语义保持现有约定。
13. **升级流程**：允许维护窗口内短暂离线。先验证目标制品，再停止服务、备份配置、切换稳定入口、重启并检查节点和模型请求。制品获取或校验失败不得影响旧实例，服务与主机重启后保持所选版本。内建自升级保持关闭。
14. **回退与卸载**：保留旧版本与配置备份，按目标发布的配置和状态兼容性执行手动回退；不承诺任意版本均可回退，不用旧身份快照覆盖仍有效的身份。卸载默认保留配置和身份，清理 Agent 状态须另行显式选择，CLI 账户目录始终保留。平台侧禁用或删除节点是独立操作。
15. **安全与资源边界**：沿用鉴权、运行时允许列表、错误脱敏以及请求、队列、并发和流式背压边界。制品摘要用于损坏检测，发布来源仍须可信；不能以摘要存在替代来源信任，也不为部署关闭 TLS 校验或系统访问限制。
16. **共享与文档**：与桌面端规格复用制品命名、版本、校验、身份保留和配置语义，按操作系统实现安装与生命周期。同步 Agent、部署、发布和服务状态说明；公共配置或行为变化同步对应使用说明。两份规格分别维护验收结果，不以另一平台的完成代替本规格验收。

## Testing Decisions

1. **测试边界与确认依据**：沿用已确认 Q12、Q13 的边界，以安装维护的公开动作和 Gateway 普通/SSE 请求作为主要行为验收入口。优先使用已有公开接口；只在定位身份、配置或 CLI 边界失败时补充该边界测试，不增加仅为检查私有函数或内部调用顺序而存在的测试。
2. **安装维护集成**：通过受控制品来源、临时安装位置和可控系统命令依赖，验证指定版本安装、摘要/架构错误、重复执行、权限失败、升级前校验失败保护、回退、卸载与身份保留。断言最终运行版本、有效配置、身份可重载、用户可见错误和相关外部状态，不要求内部文件操作顺序固定。真实 systemd 行为另在完整目标系统验证。
3. **身份公开边界**：复用现有 IdentityManager 的注册、重载、续期和 TLS 行为，以及假 Registry 与可推进时钟。参考 TestIdentityBootstrapStoresCertificateAndConsumesToken、TestIdentityRejectsLooseFilePermissions、TestIdentityLoadReusesStoredCertificate、TestIdentityRenewalRotatesKeyAndPresentsCurrentCertificate，覆盖受限文件、可写父目录、令牌消费及密钥不出网。
4. **配置与页面**：复用配置加载/保存和设置页的公开 HTTP 行为。参考 TestSaveLoadRoundTrip、TestIndexHandlerSaveAndReload、TestServeRejectsNonLoopback，检查配置持久化、运行时调和、回环约束、错误不暴露内部敏感内容及既有需重启范围。
5. **推理与 CLI**：沿用 Gateway 到 Agent 的端到端测试风格和离线 CLI 子进程夹具。参考 TestCodexResponsesThroughTunnel、TestClaudeChatThroughTunnel，以及 CLI 的 TestRuntimeLifecycle、TestRuntimeBackpressureAndClose，验证模型发现、普通/流式输出、取消、背压、关闭及已有续接约束。默认测试使用本地合成后端与受控进程，不依赖真实 Gateway、账户、外网或 GPU。
6. **时间和清理**：到期、续期、退避和超时逻辑注入 runtime.Clock，不用真实休眠推进逻辑时钟。进程生命周期测试验证实际终止和回收；协程泄漏断言沿用包级 TestMain，不在每个用例重复。测试用例使用可读名称，失败结果包含预期和实际值。
7. **完整系统矩阵**：以下六个组合逐一完成安装、开机启动、普通用户身份、节点注册/续期/重连、普通及 SSE 请求、配置操作、停止清理、升级回退和重启版本保持的记录。容器、交叉编译和架构模拟不代替原生系统或完整虚拟机结果。

   | Ubuntu | CPU | systemd/节点/请求验收 | 真实 CLI |
   | --- | --- | --- | --- |
   | 22.04 LTS | amd64 | 待验证 | 待验证 |
   | 22.04 LTS | arm64 | 待验证 | 待验证 |
   | 24.04 LTS | amd64 | 待验证 | 待验证 |
   | 24.04 LTS | arm64 | 待验证 | 待验证 |
   | 26.04 LTS | amd64 | 待验证 | 待验证 |
   | 26.04 LTS | arm64 | 待验证 | 待验证 |

8. **真实环境门控**：真实 HTTP/API、Codex、Claude 分别记录准确版本、安装和登录方式、系统账户、账户目录、架构、模型目录及调用结果。未具备环境或未通过的组合保持待验证；后端暂不可用与 Agent 启动失败分别记录。对多个运行时验证一个后端故障不隐藏其他健康后端。
9. **回归与质量门禁**：实施后完成改动相关测试及仓库要求的 Go 格式、静态检查、构建、生成一致性、默认测试与服务 race 门禁；涉及共享包时补充其格式和 race。保留现有 macOS、手动启动和既有 CLI 用法的回归结果。

## Out of Scope

- Agent 脱离 Registry/Gateway 独立提供公开推理 API，或在 Ubuntu 节点部署整个平台。
- Windows、Kubernetes、Agent 容器安装及桌面用户会话启动；它们不属于本规格的 systemd 交付范围。
- 跨 0.x minor 的生产互通承诺、内建自动升级、任意版本回退及无停机单节点升级。
- 自动安装、登录、迁移或升级 CLI、模型、Docker、GPU 驱动和推理后端；完整 Managed ComfyUI/GPU 验收。
- 将规格发布解释为已执行安装、修改平台配置、推送发布或提交代码。

## Further Notes

- 来源为本功能已确认部署方案 Q1–Q13；具体安装布局和操作背景沿用该方案。本规格以稳定的行为、职责和验收要求描述交付，保留原方案作为讨论与代码调查依据。
- 原方案已经确认测试范围和完成标准，本次沿用其高层验收边界，不重新采访或追加需求决策。
- 现有 Linux 构建配置、默认测试机制和历史联调结果不证明六个系统组合已完成验收。规划阶段未执行本规格的 Ubuntu 实机矩阵，本次只发布规格。
- 本规格与 Windows/macOS 规格分别可供后续拆分任务，复用共享改动时协调相同语义；不自动将两端实机验收设为彼此的前置条件。
- 后续阶段继续区分“代码已交付”“默认检查通过”“完整系统与真实后端已验证”。未执行的检查不得记为通过。

## Implementation Record — 2026-10-30

用户已明确要求完成部署脚本及 Windows Go 运行期适配。本轮代码、脚本、发布打包、文档和离线验证已完成，详见 [部署入口](../../deploy/agent/README.md) 与 [验证记录](../../deploy/agent/acceptance.md)。完整系统及真实后端矩阵仍待验证，不将此记录解释为逐组合验收通过。Status 行保留规格的 triage 角色，实施进展以本段和验证记录为准。
