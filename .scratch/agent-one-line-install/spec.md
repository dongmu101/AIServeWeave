# Agent 一行命令安装规格

Status: needs-triage

更新日期：2026-10-31。依据 [plan.md](plan.md) 的 Q1–Q4 与建议取舍 D1–D8 整理；D1–D8 经确认后状态改为 ready-for-agent。

## Problem Statement

节点提供者（平台运维或商户 owner/admin）要把一台 Ubuntu、macOS 或 Windows 机器接入平台，目前必须自行获取同版本二进制、手工摆放七个文件、预先建号并执行冗长命令。普通商户无法完成，商户自有节点功能因此难以落地。

## Solution

平台站点（Gateway 公开域名）提供引导脚本、同版本 Agent 制品与兑换接口。Console 生成一次性兑换码并显示对应系统的一行命令；引导脚本识别系统与架构、下载并校验制品、兑换接入材料，再调用现有 `agent.sh`/`agent.ps1` 完成安装。制品也可从 GitHub Releases 获取；凭据可用兑换码、交互输入或令牌直写三种方式提供；现有 ZIP 与手动安装保留。

## User Stories

1. 作为商户 owner，我在「自有节点」点「添加节点」，复制一条命令到 Ubuntu 上执行，就能让节点上线。
2. 作为 Mac 或 Windows 用户，我在不提权的终端里执行对应命令，Agent 以当前用户登录后运行。
3. 作为节点提供者，我不想把凭据留在 shell 历史里，可以只执行不含兑换码的命令，在提示时粘贴兑换码。
4. 作为运维，在无法访问平台下载路径的网络中，我可以让脚本从 GitHub Releases 下载同版本制品。
5. 作为运维，我能用令牌直写命令接入，不依赖兑换接口。
6. 作为已安装节点的维护者，我执行同一脚本加 `--upgrade` 就能升级到平台当前版本。
7. 作为平台运维，我能在审计中看到兑换码的生成与兑换（只含 node_id），且兑换码泄露超时后无效。

## 接入方式总览

| 方式 | Console 显示 | 凭据位置 | 依赖接口 |
| --- | --- | --- | --- |
| 兑换码（默认） | 含 `--code` 的命令 | 命令行（一次性，兑换即失效） | 兑换 |
| 交互输入 | 不含码的命令 + 单独显示的兑换码 | 运行时从终端读取 | 兑换 |
| 令牌直写 | 含 `--node-id --token --ca-sha256` 的命令 | 命令行（注册即失效，期间可见于 ps/历史） | CA 公共接口 |
| 接入 ZIP（保留） | 下载 ZIP；脚本 `--bundle` 读取 | 文件 | 无 |

## 控制面

### 表 `agent_install_codes`（base `0013_agent_install_codes.sql`，PostgreSQL/MySQL）

| 列 | 说明 |
| --- | --- |
| `code_hash` | 兑换码规范化后的 SHA-256 十六进制，主键 |
| `node_id` | 生成时分配：运维为 `node_<32hex>`，商户为 `tnode.<tenant>.<32hex>` |
| `tenant_id` | 商户码填写；平台码为平台范围标识 |
| `registry_addr` / `gateway_addr` | 写入兑换结果的外部地址 |
| `created_by` / `created_at` / `expires_at` | 生成者与有效期 |
| `redeemed_at` | 可空；兑换成功后写入 |

兑换码：20 位 Crockford Base32（100 bit，`crypto/rand`），显示为 4 位一组以 `-` 连接；规范化为去 `-`、转大写，拒绝其他字符。数据库只存哈希，不存明文码与 Registry 令牌。同步加入 `schemacheck` 模型列表、`store` 接口、gormstore 与 memstore 实现。

### 配置 `AgentInstall`

| 键 | 默认 | 含义 |
| --- | --- | --- |
| `PublicURL` | 空 | 平台站点 HTTPS 根地址（如 `https://open7x.com`），写入生成响应 `install_base_url`；为空时两条生成接口不挂载（404），ZIP 入口不受影响 |
| `RedeemPerMinute` | `60` | 兑换与 CA 接口的部署级每分钟上限，1–10000 |

兑换与 CA 两条内部接口只要求 `Registry` 配置完整即挂载，与 `PublicURL` 无关。

### 接口

| 守卫 | 方法与路径 | 说明 |
| --- | --- | --- |
| 平台会话 | `POST /operator/v1/agent-install-codes` | `{registry_addr, gateway_addr, ttl_minutes, credential}`；`credential` 为 `code`（默认）或 `token` |
| 租户会话 owner/admin | `POST /admin/v1/nodes/install-codes` | `{ttl_minutes, credential}`；按 `TenantNodes` 生效条件挂载，先写 `tenant_nodes` 行并计入配额 |
| 共享密钥 `InternalToken` | `POST /internal/v1/agent-install/redeem` | `{code}` → 兑换结果 |
| 共享密钥 `InternalToken` | `GET /internal/v1/agent-install/ca` | `{ca_pem, ca_sha256}` |

生成响应（201，`Cache-Control: no-store`，只返回一次，均含 `install_base_url`）：
- `code`：`{credential:"code", code, node_id, expires_at}`。
- `token`：`{credential:"token", node_id, bootstrap_token, ca_sha256, registry_addr, gateway_addr, expires_at}`，直接签发绑定令牌，不写兑换码行。

兑换（200，`no-store`）：`{node_id, registry_addr, gateway_addr, ca_pem, ca_sha256, bootstrap_token, token_expires_at}`。
1. 规范化并哈希；条件更新 `redeemed_at = now WHERE code_hash = ? AND redeemed_at IS NULL AND expires_at > now`，影响 0 行即失败。
2. 商户码另查 `tenant_nodes`：节点已删除即失败。
3. `MintNodeToken(node_id, 15m)`；失败时用 `context.WithoutCancel` 把 `redeemed_at` 复位为 NULL 并返回 503。
4. 未知、已兑换、已过期、节点已删除统一返回 404 `invalid_code`，不区分原因。

限流：新增与 `signupgate` 同构的部署级 Redis 固定窗口闸门，覆盖兑换与 CA 接口（默认每分钟 60 次，可配置）；Redis 故障返回 503，超限 429 + `Retry-After`。
审计：`node.install_code.create`、`node.install_code.redeem`（平台或对应租户范围），只记 node_id、到期时间与 credential 类型，不记兑换码或令牌。删除商户节点时同时使其未兑换的码失效（依赖第 2 步，不需另行写入）。
TTL：沿用 15–1440 分钟；Console 默认 60 分钟。

## Gateway

仅当配置了 `-agent-dist-dir` 与 `-agent-public-url` 时挂载，与 `mountDocs` 一样位于 API Key 中间件之外：

| 路径 | 说明 |
| --- | --- |
| `GET /agent/install.sh`、`GET /agent/install.ps1` | 引导脚本；占位符 `__AISW_PLATFORM_URL__` 替换为 `-agent-public-url` |
| `GET /agent/v1/manifest` | `{version, platform_url, github, assets:[{os, arch, name, sha256}]}`；`github` 仅在版本为正式 `vX.Y.Z` 且配置了 `-agent-github-repo` 时给出 `{repo, base_url}` |
| `GET /agent/download/<version>/<file>` | 只服务当前版本目录中的制品、`agent.sh`、`agent.ps1`、`SHA256SUMS.txt`；其他版本与路径 404 |
| `POST /agent/v1/redeem` | 请求体至多 1 KiB，转发控制面兑换接口；不记录请求体与响应体 |
| `GET /agent/v1/ca` | 转发控制面 CA 接口 |

制品目录结构 `<dist>/<version>/`：`aiserveweave-agent_<version>_<os>_<arch>[.exe]`、`agent.sh`、`agent.ps1`、`install.sh`、`install.ps1`、`SHA256SUMS.txt`（覆盖目录内全部文件）。版本以 Gateway 自身 `main.version` 为准，目录缺失时 manifest 返回 503 并在启动日志说明。错误文本经 `runtime.Redact`，兑换码与令牌不进日志。

## 构建与发布

- 根 `Dockerfile`：`SERVICE=aiServeWeaveGateway` 时增加构建阶段，按 `AGENT_PLATFORMS`（默认六个平台）以同一 `VERSION` 交叉编译 Agent，复制 `deploy/agent/{agent.sh,agent.ps1,install.sh,install.ps1}`，生成 `SHA256SUMS.txt`，落到 `/opt/aisw/agent-dist/<VERSION>/`。其他服务镜像不变。
- `deploy/docker-compose.yaml` 给 Gateway 增加 `-agent-dist-dir=/opt/aisw/agent-dist`、`-agent-public-url=${AGENT_PUBLIC_URL}`、`-agent-github-repo`；`.env.example` 与 `install.sh`/`upgrade.sh` 补默认值。
- `scripts/build-release.sh` 与 `release.yml`：Release 额外上传 `agent.sh`、`agent.ps1`、`install.sh`、`install.ps1`，并纳入总 `SHA256SUMS.txt`，使 GitHub 布局与平台目录一致。
- `-dirty` 版本：manifest 照常返回，引导脚本明确报错（`agent.sh` 不接受 dirty 版本）。

## 引导脚本

### `deploy/agent/install.sh`（Ubuntu、macOS）

整个脚本包在 `main "$@"` 中，最后一行调用，避免管道执行时被截断。

| 参数 | 说明 |
| --- | --- |
| `--code CODE` | 兑换码 |
| （无凭据参数） | 从 `/dev/tty` 提示输入兑换码，不回显；无终端时报错 |
| `--node-id ID --token TOKEN --ca-sha256 HEX` | 令牌直写；从 `/agent/v1/ca` 取 CA 并核对指纹 |
| `--bundle ZIP` | 读取接入 ZIP（需 `unzip`） |
| `--source platform\|github\|https://…` | 制品来源，默认 `platform` |
| `--version VER` | 默认取 manifest 版本 |
| `--platform URL` | 平台地址；平台下发的脚本已内置，从 GitHub 取脚本时必填 |
| `--user NAME` | Ubuntu 运行账户，默认 `aiserveweave`，不存在则 `useradd --system --create-home --shell /usr/sbin/nologin` |
| `--path PATH` | 透传给 `agent.sh` |
| `--upgrade` | 已安装节点升级到目标版本，调用 `agent.sh upgrade`，不需要凭据 |

流程：
1. 检测系统与架构；Ubuntu 要求 root，macOS 拒绝 root（与 `agent.sh` 一致），给出正确命令提示。
2. 检查依赖；Ubuntu 缺 `file`/`unzip` 时以 `apt-get install -y --no-install-recommends` 安装，macOS 只提示。
3. 获取 manifest，确定版本与资产名；在 `mktemp -d`（0700）中下载制品、`agent.sh`、`SHA256SUMS.txt`（只接受 HTTPS，`--proto =https`），先校验 SHA256。
4. 已存在安装时拒绝新凭据接入并提示 `--upgrade`。
5. **下载与校验全部成功后**才兑换凭据（避免下载失败白白消耗兑换码）；CA 与令牌写入临时目录 0600。
6. 调用 `agent.sh install --version --file --checksums --node-id --registry --gateway --ca-file --token-file [--user --path]`。
7. 退出时清理临时目录；打印节点 ID、`agent.sh status` 摘要与设置页访问方式。任何输出都不含兑换码与令牌。

### `deploy/agent/install.ps1`（Windows）

- 无顶层 `param` 必填项；支持 `-Code`、`-NodeId -Token -CaSha256`、`-Bundle`、`-Source`、`-Version`、`-Platform`、`-Upgrade`。
- 推荐调用：`& ([scriptblock]::Create((irm <url>/agent/install.ps1))) -Code X`；`irm <url>/agent/install.ps1 | iex` 进入交互输入（`Read-Host -AsSecureString`）。
- 所有逻辑在函数作用域内，不泄漏 `Set-StrictMode`、`$ErrorActionPreference`；拒绝提权窗口。
- 流程同上，最后以本地 `-File -Checksums` 模式调用 `agent.ps1 install`。
- `agent.ps1` 同步支持 7–40 位提交号版本（仅本地文件模式），与 `agent.sh` 规则一致，否则源码部署的平台无法接入 Windows。

## Console

- 运维「Agent 接入」与商户「自有节点」增加「添加节点」对话框：选择系统（Ubuntu / macOS / Windows）、接入方式（兑换码 / 交互输入 / 令牌直写）、制品来源（平台 / GitHub），显示复制即用的命令与到期时间；ZIP 下载入口保留为「高级」。
- 命令中的平台地址取自生成响应的 `install_base_url`（控制面配置 `AgentInstall.PublicURL`），Console 不直连 Gateway 读取 manifest（Console AGENTS 约束）。
- `upstream-routes.ts` 增加两条写路由；`lib/console/` 增加响应契约镜像与测试；生成只发一次、不自动重试，离开页面后不可找回。

## Testing Decisions

- 控制面：兑换码生成/规范化/哈希；兑换的成功、重复、过期、未知、节点已删除、Mint 失败回滚、并发兑换只成功一次（`-race`）；限流 429/503；审计不含码与令牌；token 模式；配额计数；gormstore 迁移在 PG/MySQL 双方言的 SQL 检查；memstore 一致性。
- Gateway：未配置时不挂载；脚本占位符替换；manifest 内容与 dirty/缺目录行为；下载路径穿越与跨版本 404；兑换转发的请求体上限、错误映射、日志不含请求体；不经过 API Key 中间件。
- 脚本：`deploy/agent/tests` 增加以 stdin 管道执行 `install.sh --stage` 的用例，本地 HTTPS 桩或注入的 manifest/兑换响应；覆盖三种凭据、两种来源、下载失败不兑换、已安装拒绝、`--upgrade`、输出不含码/令牌；PowerShell 用 scriptblock 方式执行相同场景；`agent.ps1` 提交号版本。
- Console：契约解析、命令拼装（引号与转义）、路由允许列表、不重试。
- 真实环境：在 `open7x.com` 上按 [acceptance.md](../../deploy/agent/acceptance.md) 新增「一行命令安装」列，三系统各验收一次兑换码与交互方式。

## Out of Scope

- 自动升级、包管理器（apt 源、Homebrew、winget）分发、代码签名与公证。
- 按 IP 的限流与可信代理配置。
- 安装后自动配置 CLI 或 ComfyUI 运行时（Ollama/vLLM/SGLang 已由自动发现登记）。
- 重装既有节点身份（仍为新节点；旧节点由删除流程处理）。

## Comments
