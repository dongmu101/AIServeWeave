# Agent 一行命令安装方案

Status: needs-triage

更新日期：2026-10-31。本文记录需求来源、已确认决定、现状调查与待确认事项；实施规格见 [spec.md](spec.md)，任务拆分见 [issues/](issues/)。代码尚未开始。

## 背景

现有接入流程（[Agent 部署指南](../../deploy/agent/README.md)）要求节点提供者：自行取得与平台同提交的 Agent 二进制（仓库尚无 Release tag 时只能自己交叉编译）、对齐版本号、把二进制、清单、脚本与接入 ZIP 中四个文件放到同一目录、手动创建运行账户，再执行带十余个参数的 `agent.sh install`。商户自有节点开放后，节点提供者多为普通商户，这条流程无法自助完成。

目标体验：Console「添加节点」显示一条命令，复制到目标机执行即完成下载、校验、接入与服务注册。

```bash
curl -fsSL https://open7x.com/agent/install.sh | sudo bash -s -- --code K7QF-9M2D-X4TB-H8RW-3NPC   # Ubuntu
curl -fsSL https://open7x.com/agent/install.sh | bash -s -- --code K7QF-9M2D-X4TB-H8RW-3NPC        # macOS
& ([scriptblock]::Create((irm https://open7x.com/agent/install.ps1))) -Code K7QF-9M2D-X4TB-H8RW-3NPC  # Windows
```

## 已确认决定

| 决策 | 用户选择（2026-10-31） |
| --- | --- |
| Q1 二进制来源 | 平台镜像内置并由平台站点下载、GitHub Releases，两者都支持 |
| Q2 凭据传递 | 支持多种：一次性兑换码、令牌直接写入命令、运行后交互输入 |
| Q3 系统范围 | Ubuntu、macOS、Windows 三个系统一起做 |
| Q4 推进方式 | 先写设计与任务，确认后再实现 |

## 设计取舍（待确认，见 spec）

| 编号 | 取舍 | 建议 | 理由 |
| --- | --- | --- | --- |
| D1 | 公开入口放哪 | Gateway（`open7x.com`）新增免 API Key 的 `/agent/*` | 控制面只绑 `127.0.0.1:8090`；Gateway 已有 `/docs` 免鉴权挂载先例，且已持有 `InternalToken` 调控制面 |
| D2 | 兑换码何时签发 Registry 令牌 | 兑换时才 `MintNodeToken`，库里只存兑换码 SHA-256 | 数据库不保存任何可直接注册的凭据；兑换失败可回滚为未兑换 |
| D3 | 平台内置二进制怎么来 | Gateway 镜像构建阶段同提交交叉编译 Agent，装入 `/opt/aisw/agent-dist/<version>/` | 版本天然等于 Gateway 版本，源码部署（提交号版本）也可用；`AGENT_PLATFORMS` 构建参数可裁剪 |
| D4 | 安装脚本形态 | 新增引导脚本 `install.sh`/`install.ps1`，下载到临时目录后以本地 `--file/--checksums` 模式调用现有 `agent.sh`/`agent.ps1` | 不改动已验证的安装、升级、回退语义；提交号版本无需放开 `agent.sh` 的在线下载限制 |
| D5 | 平台地址如何进入脚本 | Gateway 按 `-agent-public-url` 替换脚本内占位符后下发；从 GitHub 取脚本时用 `--platform` 指定 | 平台下发的命令最短；GitHub 来源仍可用 |
| D6 | 兑换防爆破 | 100 bit 随机码 + 部署级固定窗口限流（仿 `signupgate`，Redis 故障 fail-closed） | Gateway 位于 Nginx 之后且无可信代理配置，按 IP 限流不可靠；码空间足以抵御固定窗口内的尝试 |
| D7 | Ubuntu 运行账户 | 未指定 `--user` 时默认 `aiserveweave`，不存在则创建带家目录的系统账户 | 普通用户无需预先建号；显式 `--user` 行为不变 |
| D8 | 一行升级 | `install.sh --upgrade` 读取平台清单版本并调用 `agent.sh upgrade` | 同一入口完成升级，不引入自升级 |

## 现状调查摘要

- **签发链路可复用**：`agentenrollment.Service.CreateNode` 完成 CA 校验、`MintNodeToken` 与打包；商户节点由 `logic.CreateTenantNode` 先写 `tenant_nodes` 再签发，配额 `MaxPerTenant`/`MaxCreatesPerDay`。
- **没有一次性短码先例**：控制面无邀请码、重置码；Registry 令牌以明文 JSON 存储。API Key 采用 SHA-256 存储，可参照。条件更新可参照 `RevokeTenantNode`。
- **限流**：只有 `signupgate`（部署级 Redis 固定窗口）；登录无限流。
- **公开路由**：控制面仅登录、注册与 Dodo webhook 免会话；Gateway 的 `mountDocs` 在 `topMux` 上绕开 API Key 中间件。
- **脚本约束**：`agent.sh` 不依赖自身路径，可经管道运行，但 macOS 拒绝 root、Ubuntu 要求账户预先存在、依赖 `file`；`agent.ps1` 拒绝提权、`$Action` 为 Mandatory（`irm | iex` 无法传参）、**不支持提交号版本**。
- **发布**：Release 只上传裸二进制与总 `SHA256SUMS.txt`，`agent.sh`/`agent.ps1` 只在压缩包内；平台镜像不含 Agent。
- **自动发现**：Agent 默认探测本机 Ollama(11434)、vLLM 系(8000)、SGLang(30000) 并自动登记；CLI 与 ComfyUI 需在设置页声明。
- **反代**：仓库内无 Nginx 配置；`open7x.com` 需把 `/agent/` 前缀转到 Gateway HTTP，部署文档需补充。

## Comments
