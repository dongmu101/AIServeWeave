# Agent 部署验证记录

## Claude 连续工具回合修复（2026-11-04）

源码修复为 `ca16c33`：按本轮公开工具 ID 集合校验结果，保留旧邮箱供迟到 MCP 回调消费；beta 等价列表正常续接，实际增删功能在完整归属、历史、结果及其他配置匹配时使用新 CLI 恢复。旧代码已在模拟 CLI 加真实 mTLS 的端到端回归中复现 HTTP 400 `the request could not be processed`，修复后的同一路径通过；Claude 包、Gateway e2e 的完整 race、20 次连续续接 race 回归及相关 vet 通过。

这属于源码与离线回归验证。正式 Release、平台内置下载包、新安装节点及已升级节点的真实长任务分别需要验证，当前不据源码通过宣称这些部署路径已验收。平台升级检查包括 `/agent/v1/manifest` 的版本、Agent 文件及 `SHA256SUMS.txt`；已有节点另查 Agent 自身版本。部署与分发范围见 [升级说明](README.md#claude-code-长工具链-400-修复)。

## 文件和视频附件（2026-11-04）

新增本地附件转换模块，需在实际 Agent 上配置 LibreOffice、Poppler、FFmpeg/FFprobe、whisper.cpp 与模型，见 [配置与限制](../../service/aiServeWeaveAgent/attachments/README.md)。默认测试使用模拟转换子进程，覆盖 Office、PDF、视频画面和语音、取消/清理、资源边界及跨副本真实 mTLS 续接。开发机 Claude Code 2.1.318 已通过真实 CLI 加回环模拟 API 的 PDF 传输契约测试，不使用实际订阅，也不验证理解效果。

本次检查：`go test -p 1 ./service/aiServeWeaveAgent/... ./service/aiServeWeaveGateway/httpapi ./service/aiServeWeaveGateway/e2e -count=1` 通过；附件、Claude、Gateway e2e 的最终 race 通过，配置与设置页的 race 亦通过；相关 `go vet` 和 Linux/Windows amd64/arm64、macOS arm64 构建检查通过。新增硬输出上限测试曾发现嵌入 `bytes.Buffer` 导致 `ReaderFrom` 快路径绕过 `Write`，已改为显式缓冲字段，回归通过。并行全量测试另曾遇到未修改模块的模型下载并发测试抖动（独立 20 次复测通过）及既有 `TestManagerDrainAllStopsDispatchAndWaitsForInFlight` 排空竞态；未将它们计为附件回归，也未据串行通过声称排空竞态已修复。

| 验收项 | 状态 |
| --- | --- |
| 真实 LibreOffice 的 DOC/DOCX、XLS/XLSX、PPT/PPTX 导出与页数限制 | 待目标 Agent 验证 |
| 真实 Poppler 的 PDF 文字、扫描页、表格和图表 | 待目标 Agent 验证 |
| 真实 MP4/MOV/WebM、无音轨、含语音及多语言转录 | 待目标 Agent 验证 |
| Windows/macOS/Linux 的转换程序入口、带空格路径、取消和进程回收 | 待各原生系统验证 |
| 已登录 Claude 对 PDF/Office/视频的理解及线上入口 | 未验收，未部署 |

以上不改变下方既有部署矩阵与历史验收记录。

更新：2026-11-01。构建、离线测试、服务管理器与真实后端验收分别记录。本文不把尚未运行的 CI 记为通过。

| 目标系统 | CPU | 完整系统安装/启动/身份/普通与 SSE/升级回退 | 一行命令安装 | 真实 Codex/Claude |
| --- | --- | --- | --- | --- |
| Ubuntu 22.04 | amd64 / arm64，分别记录 | 待验证 | 待验证 | 待验证 |
| Ubuntu 24.04 | amd64 / arm64，分别记录 | 待验证 | 待验证 | 待验证 |
| Ubuntu 26.04 | amd64 / arm64，分别记录 | 待验证 | 待验证 | 待验证 |
| Windows 11 25H2 | amd64 / arm64，分别记录 | 待验证 | 待验证 | 待验证 |
| Windows 11 26H2 | amd64 / arm64，分别记录 | 待验证 | 待验证 | 待验证 |
| Windows 11 26H1 新硬件专项 | arm64 | 待验证 | 待验证 | 待验证 |
| macOS 15 | arm64 | 待验证 | 待验证 | 待验证 |
| macOS 26 | arm64 | 待验证 | 待验证 | 待验证 |
| macOS 27 | arm64 | 待验证 | 待验证 | 待验证 |

每个系统/架构组合使用原生系统或完整虚拟机独立记录：系统准确版本与硬件、Agent/平台版本及制品 SHA256、安装账户、CLI 版本/架构/安装方式/登录结果、配置路径（不含秘密）、时间、结果、脱敏证据位置。

验收顺序：

1. 在线或本地安装指定版本；错误摘要/错误架构不激活。重复安装保留配置、身份、多账户声明；重启后仍运行选定版本。
2. Ubuntu 无需用户登录即可启动且 UID 非 root；桌面仅指定用户登录后运行。Windows 验证不提权、任务无限执行/电池/重试/重复实例，运行无可见窗口。
3. 受限身份目录可注册、消费令牌、重载证书、续期与重连；Windows 故意扩大 ACL 后应拒绝，恢复安全权限后按账户处置。
4. Gateway 普通及 SSE 请求；HTTP/API 与真实 Codex、Claude 分开验证。多账户隔离，一个后端失败不隐藏其他后端，允许列表不扩大。
5. YAML/UI 编辑及运行时 reload；身份/连接修改仍需重启；设置页和指标均无公网监听。
6. 锁屏、睡眠/休眠、唤醒重连、注销和下次登录；不修改睡眠策略。
7. 取消、长流、启动后立即停止、崩溃恢复，确认 CLI 所有后代已回收；有界退出、日志上限。Windows ARM64 原生 CLI 与 x64 仿真分别记录，不能混为原生验收。
8. 校验失败升级保护、成功升级、停止确认、手动兼容回退；身份不回滚；服务与系统重启版本保持；卸载保留状态与 CLI 登录。

本次开发机检查结果在交付时填入下方，真实系统矩阵保持待验证。

## 本次开发机检查

开发机为 darwin/arm64、Go 1.27.3；未在该机器注册 systemd、LaunchAgent 或计划任务，未读取真实 CLI 登录。

| 检查 | 结果 |
| --- | --- |
| gofmt（service/api/common）、go vet ./...、go build ./... | 通过 |
| go test ./... | 通过，真实后端/数据库门控未启用 |
| go test -race ./service/... ./common/runtime | 通过；审查修正后另重跑 Agent 与共享 runtime race |
| go generate ./api/... | 生成内容一致；本机 protoc 7.35.1 相对记录的 7.34.1 仅修改两处版本注释，已核对并撤回头部差异 |
| Bash 语法与 4 项黑盒暂存回归 | 通过：损坏保护、错误格式、重复清单、重复安装/回退/卸载保留身份；使用带空格临时路径 |
| PowerShell 安装器/测试/嵌入启动器解析、参数数组检查 | 通过；使用经官方摘要验证的临时 PowerShell 7.6.9。未替代 Windows PowerShell 5.1 原生执行 |
| Windows amd64/arm64 Agent 与测试包交叉构建 | 通过；`go test -exec=true` 只证明测试包可编译，未执行 Windows 测试 |
| 发布脚本完整矩阵、6 个 Agent 压缩包与 SHA256 | 通过；含保留的 Intel Mac 制品，不代表其安装脚本支持 Intel |
| 新增原生 CI | 已配置 Windows amd64/arm64、macOS 与 Ubuntu 暂存；本轮未触发远端 CI |
| Windows ACL/pipe/Job 原生测试、完整 OS/真实 CLI | 未运行，待目标环境或 CI 执行 |

## Standards

独立标准审查发现 1 项：npm `.cmd` 原先先读取整个文件再判定 64 KiB 上限，违反缓冲有界要求（P2）。已改为 LimitReader，先关闭句柄再解析；复核无阻断残余。未报告额外 baseline smell。

## Spec

独立需求审查发现 1 项实现错误：Windows 恢复 pending 身份时仍持有未共享删除的句柄，导致删除失败（P1）。已在完成替换前关闭句柄，现有身份恢复回归覆盖此边界。另补上 macOS 停止后 PID 退出等待，以及 Windows Job 嵌套/owner 崩溃/受限创建安全失败、匿名主体 pipe 拒绝、占用替换保留与重试测试；这些 Windows 测试已交叉编译，原生执行待验证。私有后台环境 JSON 经定向复核，无新增阻断问题。

审查合计：Standards 1 项（P2，已修正），Spec 1 项实现错误（P1，已修正）；两轴复核均无阻断残余，原生验收仍按上表记录。

## 提交号安装与 763e0a2 交付（2026-10-30）

实际平台版本为 `763e0a2`，原 Bash 安装器只接受 `vX.Y.Z`，会拒绝该平台对应的本地制品。现支持 Ubuntu/macOS 的本地提交号安装与保留版本回退，仍要求所选文件的 SHA256、架构及内嵌版本匹配；提交号不走在线下载，不接受浮动或 dirty 版本。

检查结果：Bash 语法和 `git diff --check` 通过，黑盒暂存测试扩展为 7 项，包含提交号安装/回退保留身份、拒绝提交号在线或混合下载、拒绝非法版本。暂存不执行二进制，不代表目标系统运行通过。

从固定源码提交 `763e0a2b4080b3771532a12fd5ae2656f99b8fef` 的 Git 归档交叉构建 Linux amd64/arm64 Agent，内嵌版本为 `763e0a2`，CGO 关闭；`file` 核对为对应架构的静态 ELF。配套包带更新后的 Bash 安装器及二进制 SHA256 清单，不含节点凭据。目标 Ubuntu 的 systemd、注册与真实后端仍待部署验收，不因构建通过更新上方实机矩阵。

## 一行命令安装（2026-11-01）

**两类机器，别混：**

| 机器 | 做什么 |
| --- | --- |
| 平台服务器（运行 Docker Compose 的那台） | `git pull`、`deploy/enable-agent-install.sh https://站点`、配置 Nginx `location /agent/`；用 `curl https://站点/agent/v1/manifest` 确认 `version` 不是 `dev` 且 `installable` 为 `true`。**不在这台机器上装 Agent** |
| 节点机器（Ubuntu、macOS、Windows） | 执行 Console「添加节点」生成的一行命令；在这台机器上看 `systemctl status aiserveweave-agent`（Ubuntu）或 `agent.sh/agent.ps1 status`；节点要有推理后端（如 Ollama）和模型，`/v1/models` 才会列出模型 |
| 浏览器 | 登录 Console 生成命令，在「节点管理」看节点是否在线 |

「一行命令安装」列单独记录：从 Console 生成命令、在目标系统执行 `install.sh`/`install.ps1`、下载并校验制品、兑换一次性码、服务注册与启动、节点上线。通过标准是该系统/架构组合上由 Console 生成的真实命令执行成功，且节点在「节点管理」在线；暂存测试、容器内测试与 CI 不计入。逐项步骤见 [任务 08](../../.scratch/agent-one-line-install/issues/08-acceptance-open7x.md)。

补充验收项（每个组合各自记录）：

1. 兑换码方式、交互输入方式、令牌直写方式各一次；重复使用同一个码被拒绝（`invalid_code`），过期码被拒绝。
2. `--source platform` 与 `--source github`（仅正式版本）各一次；篡改 `SHA256SUMS.txt` 或二进制时安装中止且兑换码未被消耗。
3. `--upgrade` 升级已装节点，保留配置、身份与 CLI 登录。
4. Ubuntu 经 `sudo bash` 执行，服务账户非 root；Windows 在管理员窗口运行被拒绝。
5. 脚本、终端回显与平台日志中均不出现兑换码、令牌或 Prompt。

本次开发机检查（均不等于上表的实机通过）：

| 检查 | 结果 |
| --- | --- |
| `install.sh` 黑盒测试（`test_install_sh.py`，17 项，本机 HTTPS 桩平台） | 通过：下载失败与校验和不符不兑换、码被拒绝不安装、令牌方式固定 CA 并拒绝错误指纹、ZIP 方式、凭据方式互斥、终端隐藏输入、已安装节点须 `--upgrade`、自定义 HTTPS 来源、dirty/dev 版本与明文 HTTP 平台被拒 |
| `install.Tests.ps1`（PowerShell 7.5，Linux 容器） | 通过。未在 Windows PowerShell 5.1 与 Windows 主机运行 |
| `deployment.Tests.ps1` 提交号版本新增用例 | 已编写，未在 Windows 运行 |
| shellcheck | 本机未安装，未运行 |
| Gateway 镜像构建（含 Agent 制品）、`build-release.sh` 单平台、compose 渲染 | 通过；六平台镜像与真实服务器构建时长未验证 |
| Console 单测 312 项、`eslint`、`next build`、`tsc` | 通过；浏览器渲染、剪贴板与真实控制面端到端未验证 |
| `0013` 迁移（PostgreSQL/MySQL） | SQL 列检查通过，未在真实数据库上执行 |
| CI | 已在 `agent-desktop.yml` 加入 `install.sh`/`install.ps1` 测试，远端未触发 |
