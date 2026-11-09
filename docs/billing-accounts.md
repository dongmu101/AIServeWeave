# Agent 账号独享包月

在点数按量计费之外，支持把 Agent 中的 **Claude Code 账号或 Codex 账号**按月独享售给一个商户。同一个 Agent 可以同时承载两种账号，每个账号作为独立商品定价、购买和续费，也可以分别售给不同商户。凭据留在 Agent 本机；控制面只保存 Agent ID、运行时 ID、名称、月价、购买商户和到期时间。

例如，同一个 `agent-one` 可以分别售卖：

| 商品 | 运行时 ID | 账号类型 | 购买后可使用者 |
| --- | --- | --- | --- |
| Claude Code 独享账号 A | `claude-a` | `kind: claude` | 购买它的商户 A |
| Codex 独享账号 A | `codex-a` | `kind: codex` | 购买它的商户 B |

商户 A 不会因此获得 `codex-a`，商户 B 也不能使用 `claude-a`。同一商户也可以分别购买两种账号；购买一个账号不会自动独占整个 Agent。账号的识别不依赖名字包含 `claude` 或 `codex`，以上 ID 只是便于识别的示例。

Agent 中的运行时配置：

```yaml
runtimes:
  - id: claude-a
    kind: claude
    account_dir: ~/.claude-a
  - id: codex-a
    kind: codex
    account_dir: ~/.codex-a
```

在运行 Agent 的同一系统用户下分别登录：

```bash
CLAUDE_CONFIG_DIR="$HOME/.claude-a" claude auth login
CODEX_HOME="$HOME/.codex-a" codex login
```

在运维计费页的「账号包月」点击「添加账号」，先选择 `agent-one`，再分别选择 `Claude Code · claude-a`、`Codex · codex-a`，各自设置月价。选择器使用 Agent 上报的真实类型，只列出 Claude Code / Codex，排除已有商品和商户自有节点；不会把其他运行时当作 CLI 账号。实际 Agent ID 以节点清单为准，若配置了 `gateway.allowed_runtimes`，须同时允许这两个运行时 ID。CLI 登录、模型能力与账号额度沿用各自的适配器。

Claude Code / Codex 是本地账号来源；Dodo Payments 是现有点数充值的收款通道，不提供这些账号。仅售卖账号时，在本机 `deploy/controlplane.yaml` 开启：

```yaml
AccountSales:
  Enabled: true
```

该开关独立于 Dodo，默认关闭。`Billing.Enabled: true` 也会启用账号售卖。只开启 `AccountSales` 时，运维可在计费的「商户」页调整点数余额，商户再购买或续费；不创建支付结账、支付门户或 Dodo webhook，也不启用按 token 扣费。配置变更后需重新启动控制面。已有 Docker 部署可用 `bash deploy/upgrade.sh --no-pull controlplane console` 备份并重建这两个服务；首次启用须遵守下文先升级 Gateway 的顺序。

## 使用

1. 在 Agent 上为每个实际账号声明独立的运行时 ID 和 `account_dir`，先完成对应 CLI 登录，见 [Agent 多账号配置](../service/aiServeWeaveAgent/README.md#同一-cli-的多个账户)。不要把同一实际登录通过其他运行时或其他 Agent 同时提供给共享池；平台不读取凭据，无法识别这些登录是否相同。
2. 平台运维进入 `/operator/billing` 的「账号包月」，选择 Agent、选择其上报的 Claude Code / Codex 账号，填写名称与月价（点）并上架。同一 `(node_id, runtime_id)` 只能有一个商品，保存后不能更换目标，只能调整名称、价格或上架状态。上架时该目标立即成为受管库存，即使无人购买也不会向普通商户开放。
3. 商户 owner/admin 在 `/console/billing` 用点数余额购买。余额不足时由运维调整点数余额，或在已配置 Dodo 时先充值；本功能不直接创建新的 Dodo 订阅，也不自动扣款续费。确认框显示本次扣款和计费模式变化。
4. 购买成功后，商户计费模式自动变为 `account_monthly`，只能调用自己已购且未到期的账号，使用期间不按 token 扣点数。余额可以为零；仍可充值用于续费。运维也可在商户计费列表切换此模式。已有模型授权、节点专属绑定和配额继续生效。
5. 「续费一个月」从 `max(当前时间, 本商户原到期时间)` 延长一个 UTC 自然月。目标月没有对应日期时取其最后一天，例如 1 月 31 日 → 2 月 28 日 → 3 月 28 日。每次只买一个月，累计到期时间最多提前十年。到期后可手动续费；其他商户也可购买已经到期的账号，先成功提交的交易获得使用权。

下架阻止新购买和续费，已付款的期限仍有效。到期或下架不会把账号恢复为共享账号。没有删除库存的接口。到期不终止此前已开始的请求，但不允许新的请求或工具续接；若账号重新售出，原商户此前缓存的到期时间也不能延长其权限。Agent 删除或重命名该运行时不会自动迁移权益，平台运维需保持已售账号的运行时标识和实际登录稳定。

## HTTP 契约

账号、余额概览、商户余额管理、账目和订单读取路由要求 `AccountSales.Enabled` 或 `Billing.Enabled`，两者都关闭时为 `404`；租户路由要求租户会话，平台路由要求独立平台会话。

| 路由 | 用途 |
| --- | --- |
| `GET /admin/v1/billing/accounts` | 可购买库存与自己的已购账号，`{items: [...]}`；不暴露其他商户身份或其占用中的账号 |
| `POST /admin/v1/billing/accounts/:id/purchase` | owner/admin 购买或续费一个月 |
| `GET /operator/v1/billing/accounts` | 全部库存，包括下架、占用和到期账号 |
| `POST /operator/v1/billing/accounts` | 新增库存 |
| `PUT /operator/v1/billing/accounts/:id` | 更新名称、月价、上架状态；目标身份必须与原记录相同 |

创建/更新请求：

```json
{"node_id":"agent-a","runtime_id":"claude-a","name":"Claude 独享账号 A","price_micro":100000000,"active":true}
```

`price_micro` 是每月 micro 点，正整数，上限 `1,000,000,000,000`，1 点等于 1,000,000 micro 点。库存上限 1000 个。`tnode.` 商户自有节点不允许作为平台售卖库存。Console 从 Agent 清单选择账号，没有上报时提示先接入 Agent；清单读取失败与部分失败分别提示。API 仍允许预先配置库存。上架不证明 CLI 已登录、健康或具备可路由模型，离线账号会标注离线。

购买请求：

```json
{"request_id":"fedcba9876543210fedcba9876543210","expected_price_micro":100000000}
```

`request_id` 是客户端生成的 32 位小写十六进制幂等标识，同一次购买的显式重试必须复用它。请求中不接受 `tenant_id`、期限、归属等字段。`200` 返回 `{id, account_id, price_micro, expires_at}`；相同请求再次提交返回原收据，不重复扣款和延长期限。相同标识搭配不同商户、账号或价格会拒绝。余额不足 `402`；账号被其他商户占用、价格已改变或续费上限冲突 `409`；未上架或不可访问 `404`；非法请求 `400`；角色不允许 `403`。Console 写入不自动重试，确认框保持打开时的人工重试沿用原标识。

账号响应为 `{id, node_id, runtime_id, name, price_micro, active, owned, expires_at?}`。平台响应可额外带 `tenant_id`；商户响应只用 `owned` 表达本人持有。已到期并重新可售的账号不会向其他商户返回原持有者及其到期时间。

## 数据、授权与升级

`billing/0004_account_sales.sql` 为 PostgreSQL 和 MySQL 新增 `account_sales` 库存、不可变的 `account_purchases` 收据和 `account_sales_lock` 库存容量锁。购买事务按商户余额 → 账号行 → 吊销 outbox 的顺序加锁，在同一次提交中完成点数扣款、归属/期限、模式切换、收据、`account_purchase` 账目和缓存失效代际。库存创建使用独立容量锁，避免与购买事务倒置加锁。账目 `ref` 是账号商品 ID；重复请求的幂等键为 `account:<request_id>`。

Key 校验新增 `accounts: {only?, managed?: [{node_id, runtime_id, expires_at}]}`：`only` 表示账号包月模式；`managed` 包含全部受管库存，对不属于该商户的账号下发零到期时间。Gateway 在目录、候选选择、重试派发和固定运行时的工具续接上检查 `(node_id, runtime_id)` 与绝对到期时间。它每次使用缓存授权时仍比较时间；账号包月商户没有有效账号时，新工作返回 `402 account_subscription_expired`。读历史、下载已有产物和取消 Job 仍可使用。控制面的商户模型清单同样过滤账号。

**升级顺序**：先更新全部 Gateway，再对控制面执行 `-migrate up`，更新控制面和 Console，最后上架账号。旧 Gateway 会忽略新增授权字段，不能承载本功能；上架库存后不要回退到不支持账号授权的 Gateway。Agent 无需为此升级，但多账号运行时声明必须已生效。运行此版本控制面前应完成迁移，启动 schema 校验会检查新表和索引。

## 验证边界

默认测试覆盖自然月月末与闰年、余额不足、并发独享、幂等购买、续费、下架保留权益、跨租户隔离、节点授权、目录过滤、缓存到期和不向共享账号回退。Console 契约、白名单、八语言词典及生产构建有独立检查；Chrome 计费专项 11 项覆盖实际类型选择、保存目标、编辑身份固定、重复商品过滤、关闭提示和原有购买续费。独立开关的 HTTP 回归覆盖无支付配置下的路由鉴权、运维余额调整、购买与账目读取。`TestLiveAccountPurchases` 复用隔离数据库助手，可通过 `AISW_MYSQL_TEST_DSN` 或 `AISW_POSTGRES_TEST_DSN` 运行双引擎事务验证；测试会创建并删除自己的临时数据库。

此功能尚未在生产环境验收；实际 CLI 凭据、上游配额、真实付款和实际部署的缓存通知仍需部署后联调。已购账号自身的上游配额耗尽时不会借用未购买账号。
