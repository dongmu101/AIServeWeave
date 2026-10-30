# Ubuntu Agent 独立部署方案

Status: ready-for-agent

更新日期：2026-10-30。Q1–Q13 已全部确认，本轮需求澄清结束；方案可供后续规格整理与实施使用，尚未进入实现。
本文中的安装脚本、服务模板与验收记录均为拟交付内容，不代表仓库已经提供这些能力或完成实机验证。

实施规格已整理为 [spec.md](spec.md)，状态为 ready-for-agent；本文保留决策与调查记录。

## 目标与已确认范围

Agent 独立部署的含义见 [术语表](../../GLOSSARY.md)。Ubuntu 节点仅部署 Agent，连接其他机器上已有的 Registry / Gateway，接入本机推理后端。

| 决策 | 已确认选择 |
| --- | --- |
| Q1 部署边界 | 仅部署 Agent，接入已有平台 |
| Q2 运行方式 | 预编译二进制和系统级 systemd 服务，支持开机启动，目标机无需 Go |
| Q3 系统兼容 | Ubuntu 22.04 / 24.04 / 26.04 LTS，amd64 / arm64；保留现有 macOS 用法 |
| Q4 后端范围 | 保留现有能力，首批验收 HTTP/托管 API 后端及 Codex / Claude CLI；其他后端分别列明依赖和验收状态 |
| Q5 本地配置 | 保留 YAML 和本地设置页；设置页只监听回环地址，通过 SSH 转发访问 |
| Q6 升级方式 | 管理员显式升级指定版本，保留旧二进制供手动回退，内建自升级关闭 |
| Q7 服务账户 | 安装时指定固定普通用户，复用该用户的 CLI 登录，明确 HOME 和 PATH |
| Q8 安装来源 | 在线下载指定版本或从本地文件安装，两条路径均校验 SHA256 |
| Q9 平台版本 | 先保证 Agent 与 Registry / Gateway 同一发布版本互通 |
| Q10 安装权限 | 管理员使用 sudo 安装系统服务，Agent 常驻进程以指定普通用户运行 |
| Q11 首次接入 | 平台管理员提供节点 ID、绑定令牌、CA 和可达端点，安装时导入，不保存平台管理员凭据 |
| Q12 验收标准 | 六个系统/架构组合逐一记录原生系统或完整虚拟机验收；构建、默认测试、真实环境结果分别记录 |
| Q13 整体确认 | 确认全部范围和落地细节，结束本轮需求澄清；实现与实机验收另行开展 |

## 当前代码依据

- [发布脚本](../../scripts/build-release.sh) 已包含 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64，并以 CGO_ENABLED=0 构建。应构建整个 ./service/aiServeWeaveAgent 包；main.go 只是入口。
- [当前部署说明](../../deploy/README.md) 的 Agent 示例主要覆盖与 Compose 同机的源码运行；尚无 Agent 专用安装脚本和 systemd 服务模板。
- [配置路径处理](../../service/aiServeWeaveAgent/config.go) 默认在工作目录查找 config.yaml；[入口](../../service/aiServeWeaveAgent/main.go) 首次缺配置时可能仅运行设置页。进程存活不能证明节点已连接。
- [CLI 账户说明](../../service/aiServeWeaveAgent/README.md) 要求使用正确的系统账户和账户目录；终端中的登录和 PATH 不会自动成为 systemd 服务的环境。
- [身份落盘](../../service/aiServeWeaveAgent/tunnel/identity.go) 会原子重写证书、私钥与 CA；父目录必须允许服务用户写入。证书和私钥要求 0600；一次性令牌注册成功后删除。
- [内建升级执行](../../service/aiServeWeaveAgent/agentupgrade/agentupgrade.go) 下载版本文件后通过 exec 切换进程，不更新固定安装路径。由此推断，直接配合固定 ExecStart 使用时，服务再次重启可能回到旧版本。
- [发布兼容政策](../../RELEASING.md) 不承诺跨 0.x minor 的生产兼容性；[CI](../../.github/workflows/ci.yml) 的 ubuntu-latest 检查不等于六个目标组合的实机验证。

## 拟交付内容

1. Agent 安装与维护脚本：指定版本安装、重复安装、显式升级、手动回退和卸载；支持在线与本地文件来源。
2. 系统级 systemd 服务模板及环境配置示例，使用固定普通用户、绝对配置路径和明确工作目录。
3. Ubuntu 独立部署文档：接入材料、网络条件、CLI 登录、SSH 设置页、排错、升级回退与卸载说明。
4. 构建和安装脚本的离线检查，以及系统/架构验收矩阵。真实账户和真实后端测试单独门控。

拟新增内容应放入 deploy/agent/，与当前用于整个平台的 deploy/install.sh、deploy/upgrade.sh 分开；同步 Agent README/STATUS、部署说明和相关发布文档。

## 安装与目录设计

以下目录、维护窗口与卸载保留规则已随 Q13 的整体审阅确认。

| 内容 | 已确认布局与权限 |
| --- | --- |
| 版本化二进制 | /usr/local/lib/aiserveweave-agent/releases/<version>/；管理员维护，服务用户只读可执行 |
| 稳定启动入口 | /usr/local/bin/aiserveweave-agent；管理员切换到选定版本，systemd 始终使用该绝对路径 |
| systemd 服务 | /etc/systemd/system/aiserveweave-agent.service；管理员维护，使用 User=指定普通用户 |
| 本地 YAML | /var/lib/aiserveweave-agent/config.yaml；服务用户可写，以支持设置页保存 |
| 节点身份 | /var/lib/aiserveweave-agent/identity/；服务用户可写，私钥和节点证书为 0600 |
| 服务环境 | /etc/aiserveweave-agent/agent.env；管理员维护、0600，明确 HOME、PATH 和需要的供应商环境变量 |
| 日志 | 标准输出和错误交给 journald；沿用现有日志脱敏规则 |

安装器检查 Ubuntu 版本、CPU 架构、systemd、指定用户及其家目录、配置可读写性、二进制架构和校验和。仅对所选制品验证对应摘要，避免因没有下载其他服务的产物而误报失败。

在线安装固定明确版本，不隐式追随 latest；本地安装不在目标机下载制品或编译代码。摘要检查用于发现文件损坏，不替代发布来源的可信性验证；制品及摘要应来自管理员认可的发布来源。

重复安装保留已有 YAML、节点 ID、证书、私钥和 CLI 账户目录；不得覆盖正在使用的节点身份。初次导入材料时限制权限，不在参数、日志或错误中输出令牌明文。重新提供绑定令牌属于显式操作。

卸载默认只停用并移除服务和安装的二进制，保留配置与节点身份。清除 Agent 状态须单独显式选择；复用的 CLI 账户目录始终不由卸载脚本删除。卸载与平台侧节点禁用/删除分别操作。

## 首次接入与日常使用

1. 平台管理员准备唯一节点 ID、绑定该 ID 的一次性令牌、公共 CA、Registry 地址和至少一个 Gateway 隧道入口。
2. 确认端点从 Ubuntu 节点可达，服务端证书 SAN 与使用的地址匹配，Gateway 通告地址也可达。Compose 的默认回环绑定不能直接供远程节点使用。
3. 管理员指定服务用户并安装制品、服务文件和接入配置。Agent 不要求目标机安装 Go、Console、数据库或平台其他服务。
4. CLI 由相同普通用户安装和登录；安装器明确服务 PATH。存在多个账户时沿用本地 account_dir，不复制凭据、不代为登录。
5. 校验配置和身份材料后启动并启用服务；分别检查 systemd 运行、平台节点在线、运行时健康、模型可调度和实际请求结果。
6. 设置页通过 SSH 将本机端口转发到服务器 127.0.0.1:8899；页面与指标继续使用回环监听。转发端口冲突可更换客户端端口。

Agent 主动出站建立隧道，无需开放 Agent 的公网入站端口。防火墙、代理、DNS 与 CA 信任分别按目标网络核对；本地 HTTP 后端的回环访问不依赖容器主机名。

设置页保存和 SIGHUP 目前只热重载 runtimes；Gateway 地址、身份、允许列表等设置仍需重启，不扩展本次热重载范围。

## systemd 与后端兼容约束

- 服务模板以最旧目标 Ubuntu 的 systemd 能力为基线；在三个 LTS 上验证，不直接照搬最新 systemd 才支持的选项。
- 指定绝对 -config、WorkingDirectory、HOME 和 PATH，不依赖交互 shell 初始化或当前终端导出的变量。
- 保留现有 SIGTERM 退出与 SIGHUP 行为。停止超时应覆盖当前运行时清理所需时间；重启和停止时验证 CLI 子进程被回收。
- 权限限制必须兼容所选用户的 CLI 程序、账户目录、临时目录及节点身份续期；不能用统一隐藏 HOME 的配置阻断已选择的 CLI 用法。
- 发布 Agent 继续关闭 CGO；CPU 指令集选用通用基线，避免构建机器的优化设置提高目标 CPU 门槛。OS 信任库、DNS 和外部可执行程序仍需分别检查。
- Agent 本体兼容不等于各后端兼容。CUDA、NVIDIA 驱动、Docker、模型、各家 CLI 的安装及其上游支持范围分别记录，安装器不自动安装或升级它们。
- 未配置 Managed Docker 时不要求 Docker/GPU；已经配置的 Managed 后端仍沿用现有失败语义，不为了服务存活而隐藏启动失败。
- CLI 首批真实验收记录 Codex/Claude 的准确版本、安装方式、CPU 架构、登录方式、账户目录及请求结果；不宣称任意上游版本都可用。

## 升级与手动回退

升级允许该节点在维护窗口内短暂离线。安装器先验证目标制品，再停止 Agent、备份本地配置、切换稳定启动入口并重启，最后验证连接和模型请求。下载或校验失败不得影响运行中的旧版本。

保留上一个版本和对应配置备份，提供显式手动切回入口；回退后重启及主机重启均须保持所选版本。升级脚本不改动 CLI 登录、节点 ID 或已持久化的身份材料。

旧二进制是否兼容新配置或状态文件，必须按目标版本发布说明检查；保留二进制不等于承诺任意版本可回退。不自动用历史私钥或证书快照覆盖仍有效的当前身份。

内建自升级保持关闭。若未来启用，需另行解决持久启动入口、升级制品信任、排空、状态兼容和重启后版本保持问题。

## 验收矩阵与完成标准

| Ubuntu | CPU | 本轮构建结果 | systemd/接入实机结果 | Codex/Claude 实机结果 |
| --- | --- | --- | --- | --- |
| 22.04 LTS | amd64 | 未执行 | 待验证 | 待验证，记录上游支持范围 |
| 22.04 LTS | arm64 | 未执行 | 待验证 | 待验证，记录上游支持范围 |
| 24.04 LTS | amd64 | 未执行 | 待验证 | 待验证，记录上游支持范围 |
| 24.04 LTS | arm64 | 未执行 | 待验证 | 待验证，记录上游支持范围 |
| 26.04 LTS | amd64 | 未执行 | 待验证 | 待验证，记录上游支持范围 |
| 26.04 LTS | arm64 | 未执行 | 待验证 | 待验证，记录上游支持范围 |

每个组合至少记录：

1. 指定版本的在线/本地安装、摘要不匹配拒绝、重复安装保留身份；重启系统后以指定普通用户启动。
2. 同版本 Registry / Gateway 下的首次注册、证书持久化、服务重启复用身份、断线重连与证书续期路径。
3. 平台节点在线、模型可调度；通过 Gateway 完成普通和 SSE 流式请求，记录具体后端及版本。默认测试使用本地合成后端，真实推理结果另列。
4. YAML 与 SSH 设置页的读取、保存、运行时热重载和需要重启的配置变更。
5. 服务停止时 CLI 子进程清理、后端暂不可用的可见状态、错误配置和证书权限错误的可诊断结果。
6. 显式升级、手动回退、服务及主机重启后版本保持；卸载保留状态，CLI 登录不受影响。

systemd 行为使用原生 Ubuntu 或完整虚拟机验收；容器、交叉编译或架构模拟不替代该结果。尚未具备目标机器或真实账户时，结果保持待验证，交付记录明确限制。

保留现有 macOS 构建、配置、CLI 和退出行为的回归验证；Ubuntu 安装脚本对非目标系统给出明确提示，不修改已有跨平台默认值来迁就 systemd。

## 本次范围外

- Agent 脱离平台独立提供推理 API；Windows、Kubernetes 和 Agent 容器部署。
- 跨 0.x minor 的平台兼容承诺、内建自动升级和无停机单节点升级。
- 自动安装或迁移 CLI 登录、模型、GPU 驱动、Docker，以及完整 ComfyUI Managed/GPU 实机验收。
- 本次讨论直接执行 Ubuntu 安装、平台配置修改、发布或代码提交。

## 参考资料

- [Ubuntu 发布周期](https://ubuntu.com/about/release-cycle)：系统生命周期与架构范围；不能据此推断 Agent 已验证。
- [Go 最低系统与 CPU 要求](https://go.dev/wiki/MinimumRequirements)：构建与 CPU 基线。
- [systemd 执行环境](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)：用户、环境和目录行为；具体指令仍须与目标版本对照。
- [Agent 当前状态](../../service/aiServeWeaveAgent/STATUS.md)、[根状态](../../STATUS.md)：区分实现交付和真实环境验收。

## Comments

- 2026-10-30：用户对 Q13 回复“确认方案，结束本轮需求澄清”。记录全部范围和落地细节已确认；本次确认不表示安装脚本已实现或 Ubuntu 实机验收已完成。
- 2026-10-30：按用户调用 to-spec，将已确认方案整理为同目录 spec.md，沿用既定测试边界并标记 ready-for-agent；未开始代码实施或 Ubuntu 实机验收。
