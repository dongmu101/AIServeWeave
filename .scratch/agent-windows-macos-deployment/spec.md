# Windows 与 macOS Agent 独立部署规格

Status: ready-for-agent

更新日期：2026-10-30。依据本功能部署方案中已确认的 Q1–Q10 整理；状态表示规格可供实施，代码适配交付和实机验收结果另行记录。

## Problem Statement

用户需要在 Windows 11 和 Apple Silicon Mac 上独立部署 Agent，接入已有平台，并在用户登录后使用本机推理后端及现有 CLI 账户提供服务。

当前 Agent 可以构建出目标架构的二进制，但 Windows 的身份权限检查、本机 CLI 进程启动和清理、环境与文件替换语义仍存在运行期缺口；已有发布范围也尚未包含 Windows。Mac 虽具备既有运行基础，仍缺当前用户后台安装、账户环境与登录/锁屏/睡眠行为的完整交付验证。仅有交叉编译结果不能证明后台节点能够安全注册、调用 CLI 或完成升级回退。

## Solution

提供面向内部受控设备的预编译压缩包、当前用户安装维护脚本与平台原生后台集成。Windows 使用原生 amd64/arm64 Agent 和用户登录触发的计划任务，Mac 使用 Apple Silicon 原生 Agent 和用户 LaunchAgent；目标机无需 Go，不要求管理员权限安装到系统目录。

首批常规矩阵为 Windows 11 25H2/26H2 的 amd64、arm64，以及 macOS 15/26/27 的 arm64。锁屏继续运行，睡眠期间不承诺在线，唤醒后恢复连接，注销退出并在下次登录恢复。保持既有节点身份、回环设置页与 CLI 账户边界，补充 Windows 原生适配和有界生命周期管理。Agent 始终原生运行；ARM64 上的 CLI 优先原生，只有符合已确认条件的官方 x64 版本才允许仿真使用并单独标注。

## User Stories

1. As a Windows node owner, I want a native Windows Agent, so that I can use Windows-local backends and CLI accounts without WSL.
2. As a Windows node owner, I want both amd64 and arm64 artifacts, so that Agent matches the native architecture of my machine.
3. As an Apple Silicon Mac owner, I want a native arm64 Agent, so that the deployment does not depend on Rosetta.
4. As a Mac owner, I want macOS 15, 26 and 27 listed as explicit targets, so that a generic Mac build is not mistaken for validation of every system version.
5. As an internal operator, I want release archives and maintenance scripts, so that I can deploy on controlled devices without a graphical installer.
6. As a device owner, I want installation confined to my user account, so that I do not need a system-level service or changes to other users.
7. As a device owner, I want exact-version online and local-file installation, so that the same release can be reproduced across different network environments.
8. As a device owner, I want checksums and native architecture verified, so that a damaged artifact or an emulated installer shell cannot select the wrong Agent.
9. As a device owner, I want Agent to start in the background when I log in, so that my node can use the account environment in which I installed the CLI.
10. As a device owner, I want no persistent console window, so that normal background operation does not require an open terminal.
11. As a device owner, I want Agent to continue after screen lock, so that locking the device does not intentionally disable the node.
12. As a device owner, I want sleep and hibernation treated as periods without an availability promise, so that installation does not change my power settings.
13. As a device owner, I want reconnection after wake or network recovery, so that the node can become available again without a new enrollment.
14. As a device owner, I want Agent to stop when I log out and resume at my next login, so that its lifetime follows my user session.
15. As a platform operator, I want Agent to join my existing same-version Registry and Gateway, so that desktop deployment uses the established platform boundary.
16. As a platform operator, I want to supply a bound enrollment token, CA, node ID and endpoints, so that installation does not need to retain platform administrator credentials.
17. As a device owner, I want repeated installation to preserve configuration and identity, so that maintenance does not replace the enrolled node.
18. As a Windows node owner, I want identity material protected by effective Windows ACLs, so that POSIX mode checks are not silently bypassed.
19. As a device owner, I want certificate renewal and interrupted writes to be recoverable, so that state persistence works under the actual operating system.
20. As a CLI account owner, I want Agent to use my intended profile, PATH, temporary directories and account directory, so that background execution can find the same installed CLI.
21. As a CLI account owner, I want trusted native executables and supported local command wrappers handled correctly, so that installation method differences do not break invocation.
22. As a CLI account owner, I want credentials managed only by the CLI, so that Agent does not copy, export or migrate my login.
23. As a CLI account owner, I want expired or inaccessible credentials reported as backend readiness problems, so that an unavailable account is not disguised as a healthy backend.
24. As a Windows ARM64 owner, I want native Agent and native CLI operation preferred, so that normal deployment uses the intended architecture.
25. As a Windows ARM64 owner, I want a separately validated official x64 CLI fallback clearly identified when a suitable native version is absent, so that compatibility claims disclose emulation.
26. As a Windows ARM64 owner, I want CLI compatibility separated from driver and GPU-backend compatibility, so that user-mode emulation is not presented as a driver solution.
27. As a device owner, I want access to the existing loopback settings page and YAML configuration, so that I can configure the node without a public management port.
28. As a device owner, I want runtime edits to reload through supported local controls while other changes require a restart, so that configuration operations match the documented boundary.
29. As a device owner, I want local status, stop and reload controls restricted to my account, so that another user cannot control my Agent through its maintenance interface.
30. As a device owner, I want CLI descendants reclaimed after cancellation, timeout, normal stop or Agent failure, so that background inference processes do not leak.
31. As a device owner, I want an enforced bounded stop procedure, so that a maintenance script does not wait forever for shutdown.
32. As a Windows node owner, I want the background task configured for long-running operation and explicit battery behavior, so that task defaults do not unexpectedly stop the node.
33. As a Mac owner, I want the LaunchAgent environment and credential access tested in my session, so that terminal success is not mistaken for background readiness.
34. As an API client, I want ordinary and streamed Gateway requests to work through the installed Agent, so that both modes are available on each validated platform.
35. As an API client, I want interrupted inference left unplayed after wake or reconnection, so that recovering connectivity does not duplicate visible output or tool work.
36. As a device owner, I want explicit upgrades that stop the selected instance before switching versions, so that Windows does not attempt to replace a running executable.
37. As a device owner, I want failed artifact validation to leave the active installation intact, so that upgrade preparation does not create an outage.
38. As a device owner, I want manual rollback and later logins to preserve the chosen version, so that a persistent launcher cannot silently start an older release.
39. As a device owner, I want uninstall to retain state by default and always leave CLI account directories alone, so that removing Agent does not delete my login or unrelated data.
40. As a device owner, I want bounded, redacted background logs, so that unattended operation does not consume unlimited disk space or expose inference inputs.
41. As an internal operator, I want system policy blocks reported without automatic security-policy changes, so that deployment respects the device's execution controls.
42. As a maintainer, I want build, native-system, CLI and real-backend results recorded separately, so that each compatibility claim has the appropriate evidence.
43. As a maintainer, I want existing Ubuntu, manual-start and Intel Mac behavior preserved, so that new desktop installation does not remove established deployment options.

## Implementation Decisions

1. **部署边界**：沿用 Agent 独立部署定义，接入已有同发布版本 Registry/Gateway。Agent 主动出站，不部署平台其他服务，不新增公开推理入口，不承诺跨 0.x minor 生产互通。
2. **目标与架构**：Windows 原生 Agent 覆盖 amd64、arm64；Mac 本次仅覆盖 Apple Silicon。常规 Windows 系统矩阵固定为 25H2/26H2，Mac 为 15/26/27。Windows 26H1 新硬件平台另列专项验证；本次不移除已有 Intel Mac 构建或既有用法。
3. **构建与发布**：遵守仓库工具链，关闭 CGO，使用通用 CPU 基线并构建完整入口包。发布 Windows 原生可执行制品并按 OS/架构区分，复用版本与校验语义。安装器识别宿主原生架构，不能依据安装 shell 的位数或仿真进程架构误选制品。
4. **安装和存储**：内部压缩包与维护脚本仅作用于当前用户，状态和版本安装区采用已确认的系统用户目录布局。解析后传入绝对位置，分别正确处理 PowerShell、XML、shell 与程序参数，不要求 Windows 符号链接权限或管理员写入。敏感状态以支持相应权限机制的本地用户存储为目标，网络、云同步和可移动存储不属于首版目标。
5. **维护动作**：提供安装、状态、启动、停止、运行时重载、指定版本升级、手动回退和卸载。支持在线与本地文件来源，均验证所选制品摘要。重复执行保留 YAML、身份和 CLI 账户；不为安装 Agent 自动安装 Go、CLI、驱动、Docker、模型或后端。
6. **后台运行体验**：使用固定启动器和明确工作目录，后台不持续显示控制台窗口。保留现有本地 Web 设置页及手动启动方式；首版不增加托盘、菜单栏或 GUI 安装器。
7. **Windows 任务**：采用绑定当前用户交互登录身份的计划任务和普通权限，不保存用户密码，不以 SYSTEM 运行。显式取消默认的 72 小时运行限制，配置单实例、异常重启的有界退避，以及电池供电下的启动和继续运行规则；不修改系统睡眠策略。
8. **Mac LaunchAgent**：绑定当前登录用户，使用固定绝对入口和明确环境。停止、卸载和升级先暂停或卸载后台注册，防止立即被重新拉起；配置错误避免高频重启。终端登录成功不直接作为 LaunchAgent 的账户可用性证据。
9. **会话规则**：锁屏保持后台进程；睡眠/休眠不承诺节点在线，唤醒后重连；注销或关机退出，下次登录恢复。凭据访问受锁屏、过期或系统策略影响时报告后端未就绪，不导出凭据或自动升权。在途请求因系统结束而中断时不承诺完成。
10. **本地控制**：Windows 使用带当前用户 ACL 的命名管道，只提供状态、停止和运行时重载动作；不提供任意命令、通用 HTTP 代理或公网管理端口。Mac 复用已有信号和后台管理机制。控制入口具备有界请求和等待，跨用户访问必须拒绝。
11. **CLI 生命周期**：Windows 使用 Job Objects 从创建阶段管理 CLI 进程集合，覆盖取消、超时、正常关闭及 Agent 崩溃后的清理，并验证计划任务父 Job 的嵌套行为。不能保证回收时明确拒绝该方式，不降级为只终止主进程。该机制用于生命周期管理，不构成沙箱承诺；相关平台差异集中在 Agent 本地实现中复用。
12. **停止期限**：正常停止先取消工作和清理，超过有界期限后终止进程树。实现必须实际消费停止截止时间，不能把既有 15 秒上下文当作保证。不用任务管理器或计划任务的强制结束替代正常停止；强制结束仍需具备后代清理和可恢复状态。
13. **节点身份**：Windows 在创建敏感目录、身份文件及临时文件时建立并验证受限 ACL，访问主体限当前用户和必要系统管理主体，不继承宽泛用户读取权限，不跳过权限校验。Mac 沿用现有 POSIX 要求。父目录支持注册和续期写入，绑定令牌在成功注册后消费。平台差异不改变密钥不出网和节点身份契约。
14. **持久化可靠性**：Windows 配置、身份续期、下载和账本的替换行为必须按系统验证，覆盖文件占用、部分写入和失败恢复；不能由 Unix 的改名经验推断安全原子性。错误保留可恢复身份，并通过现有脱敏边界呈现。
15. **CLI 环境与启动**：Agent 与 CLI 同用户，明确各系统的用户 profile、应用数据、HOME、PATH、临时目录和账户目录。Windows 环境键按系统语义处理，子进程仍使用允许列表。exe 与受信本地 cmd 入口分别处理，远程 Prompt、工作流或其他请求内容不拼入 shell 命令，保留原有工具限制及有界 I/O。
16. **ARM64 CLI 政策**：Agent 始终原生 ARM64，CLI 优先官方适配的原生版本。只有所选 CLI 缺少适配原生版本时，才允许已单独实机验收的官方 x64 版本仿真运行，记录版本、架构和模式。该政策不推广到 GPU、驱动或推理后端；未验证的后端不宣称可用。
17. **账户与后端条件**：由 CLI 自行安装、登录、读取和刷新凭据，Agent 与安装器不读取、复制、迁移或导出凭据库。保留多 account_dir 的本地边界。HTTP/API 与 Codex、Claude 优先验收，其他适配器分别列条件；不把某种安装方式需要的 Node 或外壳推断为所有 CLI 的统一依赖。
18. **设置与推理契约**：本地设置页和指标保留回环监听。页面保存与本地重载只作用于既有运行时声明，连接、身份及允许列表等继续需重启；Windows 不依赖 SIGHUP 操作。恢复节点通道不重放已中断推理或已产生输出的流，CLI 续接保留原节点、身份与历史约束。
19. **升级与回退**：允许短暂维护离线。先获取校验制品，暂停自动拉起，正常停止并确认进程树退出，再切换持久版本选择并恢复后台运行。Windows 不覆盖运行中的可执行文件，停止不完整时中止升级。获取或校验失败保留旧实例，后续登录和重启保持选定版本。内建自升级关闭。
20. **状态保留**：保留旧二进制与配置备份，按发布版本的配置/状态兼容性手动回退，不承诺任意版本均可回退，不用历史身份快照覆盖当前有效身份。卸载默认保留配置、身份和日志，清理 Agent 状态另行显式选择；CLI 程序和登录目录始终保留，平台节点管理独立执行。
21. **日志与设备策略**：后台输出保持有界日志与保留份数，遵守现有 JSON 日志和脱敏规则。不自动关闭 Gatekeeper、SmartScreen、执行策略或企业安全控制；记录真实下载渠道、隔离属性和策略阻止结果，按组织认可方式处理，不承诺任何受管设备均无需管理介入。
22. **资源与能力诊断**：Windows 资源探测的缺失状态单列，不把未支持的内存、GPU 或磁盘检测写成成功。启用模型下载磁盘余量防线时必须实现并验证对应检查或明确拒绝未支持设置。Mac GPU 未上报不解释为无 GPU；Linux NVIDIA Managed ComfyUI 支持不推广到本规格平台。
23. **模块与跨规格协作**：改动涉及 Agent 启动、身份持久化、本地配置和设置页、CLI 执行、停止控制、发布构建以及各系统安装维护集成。复用既有运行时、隧道和非 proto 契约，跨服务类型转换边界保持原样。与 Ubuntu 规格共享版本、校验和、身份保留及配置语义，操作系统生命周期分别实现，不要求数据库变更。
24. **说明同步**：实际交付同步 Agent、部署、发布和服务状态说明，以及新增本地维护接口、配置和失败语义；正式支持表只反映已完成验证的范围。

## Testing Decisions

1. **测试边界与确认依据**：沿用 Q10 已确认的整体验收要求，以当前用户安装维护动作和 Gateway 的普通/SSE 请求为主要入口。身份、配置、CLI 使用既有公开边界；Windows 本地维护以命名管道的公开动作验证。此新增入口已包含在最终确认范围中，不另造面向测试的私有接口。
2. **测试行为**：断言安装后能否接入、账户是否正确、权限是否真正阻止访问、取消后是否存在遗留进程、运行版本是否在重启后保持及状态是否可恢复。不把内部函数调用次数、字段拆分或操作顺序作为成功标准。测试夹具模拟外部协议和受信子进程，不模拟掉目标操作系统必须验证的权限与生命周期行为。
3. **既有先例**：复用 IdentityManager 的注册、重载、续期及权限测试风格，例如 TestIdentityBootstrapStoresCertificateAndConsumesToken 和 TestIdentityRenewalRotatesKeyAndPresentsCurrentCertificate；复用配置 TestSaveLoadRoundTrip、设置页 TestIndexHandlerSaveAndReload 与 TestServeRejectsNonLoopback。以这些边界验证跨系统语义，避免重新实现身份或配置业务。
4. **CLI 与前门先例**：参考 TestRuntimeLifecycle、TestRuntimeBackpressureAndClose、TestRuntimeClockDeadlines、TestCodexResponsesThroughTunnel 和 TestClaudeChatThroughTunnel 的离线进程与端到端方式。现有部分用例限 Linux/macOS，Windows 必须具备实际可执行的夹具与原生测试，不能只移除构建限制或编译测试二进制后声称覆盖。
5. **安装和维护行为**：验证在线/本地安装、SHA256/架构错误拒绝、原生架构识别、普通用户安装、中文与空格位置、重复执行、稳定入口、后台注册单实例、升级失败保护、回退和卸载保留。受控默认测试不更改真实后台注册、全局 PATH、系统安全策略或真实用户凭据；操作系统后台管理结果在隔离完整系统中验收。
6. **Windows 原生安全与清理**：在目标 Windows 验证敏感文件和命名管道的有效 ACL、其他用户访问拒绝、父目录及临时文件权限、占用/替换失败恢复；验证 Job Objects 创建阶段关联、父 Job 嵌套、后代清理、Agent 强制结束、cmd 入口参数与环境大小写。真实安全上下文测试门控执行，不将字符串形式的权限配置相等当作实际访问隔离通过。
7. **时间与系统事件**：逻辑超时、退避和续期使用注入的 runtime.Clock，不用真实休眠推进测试时钟；操作系统睡眠、锁屏、注销和会话凭据行为在独立系统验收记录。协程泄漏沿用包级 TestMain，进程测试检查真实回收结果。
8. **普通和流式请求**：在同版本本地测试平台下验证节点在线、模型可调度、普通和 SSE 输出、取消与背压；一个后端不可用不掩盖其他健康后端。断线、唤醒或恢复后台任务后不重放中断请求，已有工具续接保护保持有效。默认测试不依赖付费账户、外网、真实 GPU 或生产平台。
9. **首批系统矩阵**：以下组合分别记录后台安装、节点接入、请求、配置、停止清理、会话事件、升级回退和日志结果。使用完整目标系统或完整虚拟机，不以 WSL、容器、交叉编译或另一系统成功代替。

   | 系统 | CPU | 后台/节点/请求验收 | 真实 CLI |
   | --- | --- | --- | --- |
   | Windows 11 25H2 | amd64 | 待验证 | 待验证 |
   | Windows 11 26H2 | amd64 | 待验证 | 待验证 |
   | Windows 11 25H2 | arm64 | 待验证 | 待验证，记录原生/仿真模式 |
   | Windows 11 26H2 | arm64 | 待验证 | 待验证，记录原生/仿真模式 |
   | macOS 15 | arm64 | 待验证 | 待验证 |
   | macOS 26 | arm64 | 待验证 | 待验证 |
   | macOS 27 | arm64 | 待验证 | 待验证 |

10. **系统特有验收**：Windows 检查长期任务、电池条件、登录触发、重复登录、正常本地停止及无遗留子进程；Mac 检查 LaunchAgent 环境、锁屏后的 CLI 凭据访问、停止/升级防重拉起。两端检查睡眠/唤醒、注销/重登、网络恢复、端口冲突和真实下载渠道下的系统执行控制。未执行或策略阻止的结果如实记录。
11. **真实 CLI 和后端**：记录系统 edition/build、硬件、Agent 版本、CLI 版本及完整安装方式、登录账户目录、模型发现、额度与实际推理结果。Windows ARM64 的仿真 CLI 单独记录，不用于证明原生模式或 GPU 后端通过。凭据过期、缺失或拒绝访问时验证可诊断未就绪状态；CLI 的上游系统条件逐版本核对。
12. **质量门禁与回归**：实施后执行相关原生测试和仓库要求的 Go 门禁；共享包变化补格式和 race。覆盖现有 Linux/macOS 手动运行与原有 Intel Mac 行为，新增 Windows 本地控制不改变 Gateway 隧道控制或其他系统退出语义。

## Out of Scope

- Windows 10、Windows Server、WSL Agent、Windows 系统服务、Mac LaunchDaemon、登录前持续在线和修改系统睡眠设置。
- 新增 Intel Mac 部署支持承诺，macOS 13/14 首版适配；已有 Intel 构建不因此删除。
- 将 Windows 26H1 自动纳入常规系统矩阵；该硬件平台另行验证后才能扩展支持范围。
- GUI 安装器、托盘或菜单栏应用、管理员统一多用户安装、网络或可移动敏感状态存储、面向外部用户的签名/公证发布承诺。
- Agent 脱离平台提供公开推理 API、跨 0.x minor 生产互通、内建自动升级、无停机升级或任意版本回退。
- 自动安装、登录、复制或迁移 CLI 账户、模型、驱动和推理后端，以及 Windows/Mac Managed ComfyUI 或 GPU 驱动适配承诺。
- 把 CLI 用户态仿真当作驱动仿真、把 Job Objects 当作沙箱，或关闭设备安全控制以使安装继续。
- 本次规格整理直接执行系统安装、平台配置修改、发布或代码提交。

## Further Notes

- 来源为本功能已确认部署方案 Q1–Q10；具体目录布局、调查依据与讨论记录保留在该方案中。本规格约束用户行为、模块职责和验收要求。
- 安装维护入口、命名管道控制、CLI 生命周期与完整系统验收范围已包含在 Q10 最终确认中。本次直接沿用这些测试边界，不重新采访或扩大范围。
- 讨论阶段 windows/amd64、windows/arm64、darwin/arm64 Agent 曾在现有工具链下交叉编译成功，Windows Codex 测试二进制也曾编译成功；这些结果不代表完成目标系统执行或实现后回归。后台、账户、ACL 和完整实机矩阵仍待验证。
- 当前正式发布范围仍未宣称 Windows 支持，现有 Windows CLI 路径和身份权限存在已知运行期缺口；实施需完成实际适配，不能将更新构建列表或文档作为功能完成。
- 本规格与 Ubuntu 规格复用共享语义，分别交付和记录各自系统结果；不默认要求 Ubuntu 的全部实机验收先完成，也不以桌面端结果代替 Ubuntu 验收。
- 状态 ready-for-agent 表示需求可供实施。缺少设备、CLI 账户、特定架构工件或真实后端时记录待验证及具体限制，不将未执行检查写成通过。

## Implementation Record — 2026-10-30

用户已明确要求完成部署脚本及 Windows Go 运行期适配。本轮代码、脚本、发布打包、文档和离线验证已完成，详见 [部署入口](../../deploy/agent/README.md) 与 [验证记录](../../deploy/agent/acceptance.md)。完整系统及真实后端矩阵仍待验证，不将此记录解释为逐组合验收通过。Status 行保留规格的 triage 角色，实施进展以本段和验证记录为准。
