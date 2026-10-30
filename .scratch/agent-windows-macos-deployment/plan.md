# Windows 与 macOS Agent 独立部署方案

Status: ready-for-agent

更新日期：2026-10-30。Q1–Q10 已全部确认，本轮需求澄清结束；方案可供后续规格整理与实施使用，代码适配、系统安装和实机验收尚未开始。

实施规格已整理为 [spec.md](spec.md)，状态为 ready-for-agent；本文保留决策与调查记录。

## 与 Ubuntu 方案的关系

沿用 [已确认的 Ubuntu 方案](../agent-ubuntu-deployment/plan.md) 中的平台边界：Agent 接入已有 Registry / Gateway，目标机无需部署平台其他服务，Agent 主动出站建立隧道。

本方案沿用指定版本的在线/本地文件安装及 SHA256 校验、同发布版本平台互通、导入节点接入材料、显式升级和手动回退、保留身份与 CLI 登录、本地设置页只监听回环地址、HTTP/API 与 Codex/Claude 优先验收的约定。依据 Q8，安装与维护由当前用户执行。Ubuntu 的 systemd、系统目录和开机未登录运行方式不套用到本方案。

本次新增部署方案仅覆盖用户选择的 Mac 架构；仓库已有 darwin/amd64 构建和原有 macOS 用法继续保留，不在本次讨论中删除。

## 已确认决定

| 决策 | 用户选择 |
| --- | --- |
| Q1 Windows 环境 | Windows 原生 Agent，直接运行原生 exe，使用 Windows 本机后端与 CLI 登录 |
| Q2 Mac 架构 | 仅 Apple Silicon，使用 darwin/arm64 原生二进制 |
| Q3 启动时机 | 两端均在指定用户登录后后台运行，复用该用户的账户环境 |
| Q4 Windows 范围 | Windows 11，原生 amd64 和 arm64 均纳入目标；具体发行版本记入验收矩阵 |
| Q5 Mac 系统范围 | Apple Silicon 上的 macOS 15 / 26 / 27；13 / 14 不属于首版范围 |
| Q6 交付对象 | 内部受控设备，压缩包与安装维护脚本；Mac 使用用户 LaunchAgent，Windows 使用用户登录触发的计划任务 |
| Q7 会话规则 | 锁屏后继续运行；睡眠/休眠期间不承诺在线，唤醒后重连；注销/关机退出，下次登录恢复；不修改系统睡眠设置 |
| Q8 安装权限 | 当前用户安装与运行，使用个人目录，不要求管理员权限，不修改其他用户的 CLI 登录 |
| Q9 ARM64 CLI | Agent 始终原生 ARM64；CLI 优先官方原生版本，缺少适配版本时允许经过单独实机验收并明确标注的官方 x64 仿真版本，不推广到驱动或推理后端 |
| Q10 整体确认 | 确认安装维护设计、Windows 适配范围、首批系统矩阵和验收门槛，结束本轮需求澄清；不直接开始实现 |

## 确认结果

用户已通过 Q10 整体确认本文的安装维护设计、停止及 CLI 回收要求、系统矩阵与验收门槛，无待回答的需求问题。后续代码交付和实机结果按本文分别记录。

## 当前事实与兼容缺口

| 范围 | 已查证事实 | 方案影响 |
| --- | --- | --- |
| 正式发布范围 | [RELEASING.md](../../RELEASING.md) 当前明确不支持 Windows；[发布脚本](../../scripts/build-release.sh) 只构建 linux/darwin，尚无 Windows 制品或两端后台安装模板 | Windows 原生支持属于新增适配，须同步发布和验证说明 |
| Windows 编译 | 本轮以现有 Go 1.27.3、禁用网络和 CGO，交叉编译 windows/amd64 Agent 及 Codex 测试二进制成功；Agent 产物为 PE32+ x86-64 | 仅证明可构建，未在 Windows 执行，不代表可注册、推理或管理 CLI |
| 新目标构建 | 本轮另以同一工具链和离线条件交叉编译 windows/arm64、darwin/arm64 Agent 成功，产物分别为 Aarch64 PE32+ 与 arm64 Mach-O | 三个目标均已确认可构建；操作系统、原生 CLI 与用户后台集成仍待实机验证 |
| Windows 身份文件 | [身份检查](../../service/aiServeWeaveAgent/tunnel/identity.go) 严格要求证书/私钥为 0600，并限制绑定令牌权限；Go Windows 的 Chmod 不提供这种权限隔离语义 | 必须使用 Windows ACL 建立并检查节点身份的账户访问边界，不能简单跳过安全检查 |
| Windows CLI | [Codex](../../service/aiServeWeaveAgent/codexcli/process_other.go)、[Claude](../../service/aiServeWeaveAgent/claudecode/process_other.go)、[通用 CLI](../../service/aiServeWeaveAgent/cliagent/process_other.go)、[Gemini CLI](../../service/aiServeWeaveAgent/geminicli/process_other.go) 的非 Linux/macOS 路径均拒绝启动 | 需补充 Windows 进程启动、取消、超时和整棵子进程回收语义 |
| Windows 环境与路径 | 现有 CLI 环境过滤主要按 Unix 变量处理；CLI 可执行入口可能是 exe 或 npm cmd 包装器 | 核对 Path 大小写、USERPROFILE/APPDATA/LOCALAPPDATA、临时目录、命令引用、空格和中文路径；保持原有环境隔离 |
| Windows 文件更新 | 配置、身份续期及下载状态使用临时文件再 Rename | 需验证 Windows 文件共享、替换和故障恢复行为，不能由 Unix 经验推断原子性 |
| 退出与热重载 | [main.go](../../service/aiServeWeaveAgent/main.go) 以 SIGINT/SIGTERM 退出、SIGHUP 热重载；现有页面保存也可热重载运行时 | Windows 需明确本地停止与重载入口；SIGHUP 不作为 Windows 运维契约 |
| 停止时间 | main.go 声明 15 秒清理上下文，但 [runtime manager](../../common/runtime/manager.go) 的 Close 当前未消费传入上下文 | 不承诺现有 Agent 必在 15 秒内退出；后台停止、系统注销和 CLI 回收需验收 |
| 内建升级 | [升级执行](../../service/aiServeWeaveAgent/agentupgrade/agentupgrade.go) 使用 syscall.Exec；本轮工具链 Windows 该调用返回 EWINDOWS，macOS 也不会更新持久启动入口 | 沿用已确认的显式升级与手动回退；内建自升级保持关闭 |
| macOS 账户 | [Agent README](../../service/aiServeWeaveAgent/README.md) 要求与 CLI 同用户；[Gemini 说明](../../service/aiServeWeaveAgent/geminicli/README.md) 明确终端环境不会自动进入 launchd | 后台程序须使用正确 HOME、PATH 和账户目录，验证 CLI 自行读取凭据的结果，不复制或导出钥匙串凭据 |
| 后端依赖 | 根 STATUS 中 Managed ComfyUI 的目标是 Linux NVIDIA，Mac 资源探测也不会报告通用 GPU/显存数值 | Agent 本体兼容与外部后端支持分开，不将 Mac GPU 零值解释为没有 GPU，不把 Linux Managed 支持推广到两端 |

## 当前验证记录

- Windows/amd64 Agent 交叉编译：成功；Windows 原生执行、节点注册、CLI 和后台行为：未执行。
- Windows/arm64、darwin/arm64 Agent 交叉编译：成功；未执行生成的二进制。
- Windows Codex 测试二进制交叉编译：成功；未执行 Windows 测试，现有部分进程测试只覆盖 Linux/macOS。
- macOS：本轮仅构建 Agent，未执行。仓库已有真实 CLI 链路记录，但不能扩大为 LaunchAgent、跨系统版本或注销/睡眠行为的验证。
- 本轮没有修改应用代码、安装后台服务、发布制品或运行真实账户推理。

## 上游 CLI 与 Windows ARM64 条件

- 按 Q9，Windows ARM64 上的 Agent 必须原生运行；CLI 的 x64 仿真仅在所选官方版本缺少适配原生版本且已单独实机验收时使用，记录版本、架构和运行模式。未通过的后端保持待验证，不将 Agent 可构建等同于该 CLI 可用。
- [Claude 官方安装说明](https://code.claude.com/docs/en/setup) 列出 win32-x64、win32-arm64、darwin-arm64；npm 与独立安装使用同一原生二进制。npm 安装需要 Node，但不能把 Node 或 Git Bash 写成所有 Claude 安装的统一前置条件。官方最低系统要求与本项目选择的验收版本分别记录。
- [Codex 企业部署说明](https://learn.chatgpt.com/docs/enterprise/windows-deployment#deploy-the-codex-cli) 要求保持 CLI 安装运行时文件完整，不能仅复制 codex.exe。该页面的桌面应用 MSIX 架构列表不能作为 CLI 架构清单。
- 本轮所查 OpenAI 官方文档未确定 Codex CLI 的完整架构工件矩阵；Windows ARM64 的具体 Codex 版本、发布工件及实际调用结果应在实施验收时明确，不能据此认定不支持。
- [Microsoft Windows ARM 文档](https://learn.microsoft.com/en-us/windows/arm/apps-on-arm-x86-emulation) 区分用户态 x64 仿真与原生驱动要求。Agent 原生 ARM64、CLI 原生/仿真模式、推理后端及其 GPU 驱动分别列出，不互相替代。
- [Codex 凭据缓存说明](https://learn.chatgpt.com/docs/auth#login-caching) 和 [Claude 凭据说明](https://code.claude.com/docs/en/authentication#credential-management) 表明凭据与用户/账户目录有关。由 CLI 自行读取和刷新凭据；不把 HOME/PATH 设置正确当作 Keychain、系统凭据库和过期登录一定可用的证明。

## 拟交付内容与目录设计

以下设计已随 Q10 整体审阅确认，所列脚本、控制入口和适配能力尚未实现。

1. Windows 原生适配：节点身份 ACL、Windows CLI 启动与进程树回收、本地停止控制、环境及路径处理、状态文件替换和恢复。
2. 当前用户安装、启动、状态、停止、运行时重载、指定版本升级、手动回退和卸载脚本；Windows 使用 PowerShell，Mac 使用系统 shell，不要求目标机安装 Go。
3. Windows 用户计划任务及 Mac 用户 LaunchAgent 模板，在线与本地文件制品安装、SHA256 校验和后台日志管理。
4. Windows/macOS 部署文档与逐组合验收记录；实际交付时更新 Agent README/STATUS、RELEASING、发布工作流与相关公共配置/行为说明。

拟放置于 deploy/agent/windows/ 与 deploy/agent/macos/，共享发行校验和、版本和配置语义；不改变已确认的 Ubuntu 方案或既有手动启动方式。

| 内容 | Windows 当前用户 | macOS 当前用户 |
| --- | --- | --- |
| 程序与版本目录 | `%LOCALAPPDATA%\AIServeWeave\Agent\versions\<version>` | `~/Library/Application Support/AIServeWeave/Agent/versions/<version>` |
| 配置与身份 | `%LOCALAPPDATA%\AIServeWeave\Agent\config.yaml`、`identity/` | `~/Library/Application Support/AIServeWeave/Agent/config.yaml`、`identity/` |
| 稳定启动入口 | 用户目录内的固定启动器，读取本地选定版本；不依赖 Windows 符号链接权限 | 用户目录内的固定启动器与选定版本入口 |
| 后台注册 | 当前用户 SID 对应的登录触发计划任务 | `~/Library/LaunchAgents/` 下专用 plist |
| 日志 | `%LOCALAPPDATA%\AIServeWeave\Agent\logs/` | `~/Library/Logs/AIServeWeave/Agent/` |

表中的用户目录在安装时解析为绝对路径；XML、PowerShell 参数、程序参数分别按其格式处理，不让后台任务依赖 ~、百分号变量或交互 shell 的隐式展开。程序与凭据使用本地用户存储；网络家目录、UNC、FAT/exFAT、可移动盘和云同步身份目录不作为首版敏感状态存储目标。

首版提供维护脚本和现有本地 Web 设置页，后台不持续显示控制台窗口；托盘、菜单栏程序、图形安装器、系统级服务及多用户统一安装不纳入首版。可在本机浏览器直接访问回环设置页；远程访问仍使用显式端口转发。

## 安装、账户与身份

- 初次安装导入绑定节点 ID 的一次性令牌、CA 和可达 Registry/Gateway 地址；令牌通过受限文件交付，不进入命令行明文或日志。复用已注册身份时不要求重新提供令牌。
- 为当前用户安装单个后台实例，防止重复登录触发和重复安装产生多个进程；检测端口和节点身份冲突。不同用户的状态、CLI 账户和后台注册互相隔离，不自动支持同机多用户并发使用同一节点身份。
- Windows 在创建敏感目录和临时文件时应用并检查受限 ACL，避免沿用 Users/Everyone 等宽泛读取权限；允许的访问主体以当前用户及必要的系统管理主体为限。不能以跳过 0600 检查代替 Windows 权限实现。macOS 沿用现有身份权限要求，证书、私钥及令牌父目录须支持用户写入和续期。
- Agent 和 CLI 以同一用户运行；明确 Windows 的 Path、USERPROFILE、APPDATA、LOCALAPPDATA、TEMP/TMP，以及 Mac 的 HOME、PATH 和账户目录。CLI 子进程环境继续使用允许列表，不直接透传全部 Agent 环境变量。
- CLI 由用户按官方方式安装并登录，安装器不代为安装、复制、读取或迁移 CLI 凭据，不自动升级模型、GPU 驱动或推理后端。Codex/Claude 的额度、模型发现和推理分别验证。
- exe 与受信本地 cmd 包装入口分别处理。远程请求内容、Prompt 和工作流不得拼接到 shell 命令；原有工具关闭、超时、消息大小、并发和错误脱敏边界继续生效。
- 重复安装保留 YAML、节点 ID、证书、私钥和账户目录。后台日志须限制大小与保留份数，沿用 JSON 格式及脱敏要求，不写入令牌、鉴权头或完整推理输入。

## 后台生命周期与本地控制

### Windows

计划任务绑定当前用户的交互登录身份，使用普通权限，不保存用户密码或以 SYSTEM 运行。任务动作使用固定绝对路径和工作目录；不依赖当前 PowerShell 会话、终端 profile 或全局 PATH 修改。

任务显式设置长期运行、不重复启动、异常重启的有界退避，以及电池供电下的启动/继续运行规则；不通过任务策略阻止系统睡眠。[Microsoft 文档](https://learn.microsoft.com/en-us/windows/win32/taskschd/tasksettings-executiontimelimit) 明确默认运行时限为 72 小时，需为常驻 Agent 设置无限执行时限；默认电池条件也不能直接沿用。

正常停止和重载由仅允许当前用户访问、带用户 ACL 的本地命名管道执行；只提供状态、停止和运行时重载动作，不开放通用命令或公网管理端口。停止时先取消工作并等待清理，再按有界超时终止进程树，不把计划任务的强制结束当作正常停止流程。

CLI 进程使用 [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects) 从创建阶段纳入生命周期控制；覆盖取消、超时、Agent 崩溃或被终止时的后代清理，验证计划任务父 Job 的嵌套行为。无法保证回收时应明确拒绝该 CLI 运行方式，不能降级为只杀主进程。该机制用于生命周期管理，不作为操作系统沙箱承诺。

### macOS

使用当前登录用户的 LaunchAgent，plist 指向固定绝对入口，显式配置工作目录和必要环境。配置异常不应造成高频重启；正常停用与升级需卸载或暂停后台注册，避免被守护机制立即重启。

明确区分锁屏、睡眠和注销；锁屏后 CLI 是否仍能读取自己的 Keychain 凭据必须实测。认证过期或系统策略拒绝访问时报告后端未就绪，不自动导出凭据或改为更高权限运行。

### 两端共同要求

正常停止的截止时间必须由实现真正执行；当前代码的 15 秒上下文不能作为已满足的保证。注销或关机可能被系统强制结束，在途推理不承诺完成。停止、崩溃和注销均需验证 CLI 后代无遗留。

睡眠、网络变化或断网后恢复节点通道，不自动重放已中断的推理或客户端已看到输出的流。CLI 续接继续遵守原节点、身份和历史要求。配置页保存只热重载已有 runtimes 范围；连接、身份及允许列表等仍需重启，Windows 不要求用户发送 SIGHUP。

## 显式升级、回退与卸载

固定指定版本并校验对应 OS/CPU 制品；Windows ARM64 不误取 amd64 Agent，Mac 不依赖 Rosetta。摘要检查用于发现制品损坏，制品来源仍须由操作者确认；不默默追随 latest。

安装器识别宿主系统的原生架构，不以安装 shell 的进程架构或位数代替；覆盖 Windows ARM 上的 x64 PowerShell 和 Apple Silicon 上经 Rosetta 启动的终端，避免误选或错误拒绝原生制品。

升级允许短暂离线：先下载和校验目标版本，暂停后台自动拉起，正常停止并确认 Agent/CLI 退出，再切换本地版本入口并恢复后台任务，验证节点与模型请求。Windows 不尝试覆盖运行中的 exe，无法停净则停止升级；下载或校验失败不影响旧实例。

保留上一个版本和相应配置备份，支持显式手动回退；再次登录和系统重启后仍启动所选版本。旧二进制与配置/状态兼容性按发布说明检查，不用历史身份快照覆盖当前有效证书和私钥。内建自升级保持关闭。

卸载默认只移除本用户的后台注册及程序，保留配置、身份和日志；清除 Agent 状态单独显式选择。CLI 程序及登录目录始终不由 Agent 卸载流程删除，平台侧禁用/删除节点另行操作。

内部脚本部署不自动关闭 Gatekeeper、SmartScreen、执行策略或企业安全控制。记录真实下载渠道、隔离属性及系统拦截情况；策略阻止运行时给出可诊断结果，通过组织认可方式处理，不承诺所有受管设备均能无管理员介入运行。

## 验收矩阵

延续 Ubuntu 方案的验收口径：构建、默认测试、真实系统、真实 CLI 和真实后端结果分别记录。完整 Windows/macOS 系统或对应完整虚拟机的原生内核行为才可作为后台集成证据，交叉编译不代替实机验收。

| 系统目标 | CPU | 当前构建 | 后台/接入 | CLI |
| --- | --- | --- | --- | --- |
| Windows 11 25H2 | amd64 | 成功，仅证明架构可构建 | 待验证 | 待验证 |
| Windows 11 26H2 | amd64 | 成功，仅证明架构可构建 | 待验证 | 待验证 |
| Windows 11 25H2 | arm64 | 成功，仅证明架构可构建 | 待验证 | 待验证，记录原生/仿真模式 |
| Windows 11 26H2 | arm64 | 成功，仅证明架构可构建 | 待验证 | 待验证，记录原生/仿真模式 |
| macOS 15 | arm64 | 成功，仅证明架构可构建 | 待验证 | 待验证 |
| macOS 26 | arm64 | 成功，仅证明架构可构建 | 待验证 | 待验证 |
| macOS 27 | arm64 | 成功，仅证明架构可构建 | 待验证 | 待验证 |

Windows 的首批常规验收矩阵固定为 25H2/26H2；26H1 专用新硬件平台单列专项验证，不从其他发行版本的成功推定。每份记录包含系统 edition/build、硬件架构、Agent 版本、CLI 版本/完整安装方式和后端版本。未通过的组合保持待验证，不宣称已支持；将 26H1 纳入常规支持范围需后续明确扩展矩阵并完成对应验证。

每个目标组合至少覆盖：

1. 在线/本地文件安装、错架构和摘要不符拒绝、重复安装保留身份、当前用户权限和中文/空格路径；系统重启后在登录时启动。
2. 绑定令牌注册、身份重载和证书续期、权限过宽拒绝、写入中断/文件占用失败后的身份可恢复性；不同用户访问边界。
3. 同版本平台下节点在线、模型可调度、普通及 SSE 流式请求；默认测试使用本地合成后端，真实 HTTP/API 和真实 CLI 结果另列。
4. Web 设置页、YAML、运行时热重载、需重启配置，以及端口冲突与可见错误；不以进程存活代替接入成功。
5. 锁屏、睡眠/休眠与唤醒、断网恢复、注销和重新登录；CLI 凭据过期及无法读取凭据的状态。
6. 正常停止、崩溃、超时、强制终止时的 CLI 进程树回收；在途流中断后不自动重放。
7. 指定版本升级、手动回退、重新登录后版本保持、卸载保留身份与 CLI 登录；后台日志有界。
8. Windows 计划任务的长期运行、电池条件、父子 Job 行为与账户环境；Mac LaunchAgent 环境、Keychain 与真实下载渠道的执行结果。

Windows 资源探测需单列支持状态：CPU/系统/架构已有结果，内存/GPU 和模型下载磁盘余量检查存在 Windows 缺口。若启用磁盘余量防线，先实现并验证 Windows 对应行为，不能把探测失败写成检查通过；Mac GPU 未报告也不等于没有 GPU。

保留现有 Linux/macOS 行为回归。实施时执行根 AGENTS 要求的 Go 质量门禁；涉及 common/ 时补充共享包格式和 race，新增 Windows 行为在 Windows 原生默认测试及门控集成测试中验证。上游 CLI 的真实账户和外部后端不进入默认离线测试。

## 首版范围外

- Windows 10、Windows Server、WSL Agent、Windows 系统服务、Mac LaunchDaemon 和登录前持续在线。
- 新增 Intel Mac 部署承诺，macOS 13/14 的首版适配；既有 darwin/amd64 构建不因此移除。
- GUI 安装器、托盘/菜单栏应用、面向外部用户的签名/公证发布承诺、跨 0.x minor 平台兼容承诺和内建自升级。
- 自动安装或迁移 CLI 账户、驱动、模型和 GPU 后端；把 Linux NVIDIA Managed ComfyUI 支持推广到 Windows/Mac。
- 将本轮方案确认当作代码交付、操作系统安装或实机验收已完成。

## 参考资料

- [Go 最低系统要求](https://go.dev/wiki/MinimumRequirements)：Go 1.27 的 macOS 下限为 13；工具链下限不等于本项目已验收范围。
- [Go Chmod 文档](https://pkg.go.dev/os#Chmod)：Windows 的权限位行为与 POSIX 不同。
- [Apple launchd 文档](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)：用户代理依附于已登录用户的会话。
- [Windows 任务安全上下文](https://learn.microsoft.com/en-us/windows/win32/taskschd/security-contexts-for-running-tasks)：计划任务的账户、登录类型和权限需要明确设置。
- [Windows 11 发布信息](https://learn.microsoft.com/en-us/windows/release-health/windows11-release-information)、[macOS 版本列表](https://support.apple.com/en-us/109033)：用于确定当前系统矩阵；不据此认定 Agent 已支持。

## Comments

- 2026-10-30：用户确认 Q9：“是，Agent 原生，CLI 优先原生并允许已验证的 x64 仿真”。Q1–Q9 已记录；等待完整方案的最终确认。
- 2026-10-30：用户确认 Q10：“确认方案，结束本轮需求澄清”。Q1–Q10 全部确认，方案标记为 ready-for-agent；本次确认不表示代码适配、后台安装或实机验收已经完成。
- 2026-10-30：按用户调用 to-spec，将已确认方案整理为同目录 spec.md，沿用既定测试边界并标记 ready-for-agent；既有交叉编译记录不扩大为后台或实机验收结果。
