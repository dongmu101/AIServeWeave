# Agent 账号独享包月

在点数按量计费之外，支持把 Agent 中的 **Claude Code 账号或 Codex 账号**按月独享售给一个商户。账号商品**以法币标价**（如 USD 19.99 / 月），商户经 Dodo Payments 在线结账付款，**不使用点数余额**。同一个 Agent 可以同时承载两种账号，每个账号作为独立商品定价、购买和续费，也可以分别售给不同商户。凭据留在 Agent 本机；控制面只保存 Agent ID、运行时 ID、名称、币种与月价、Dodo 产品 ID、购买商户和到期时间。

例如，同一个 `agent-one` 可以分别售卖：

| 商品 | 运行时 ID | 账号类型 | 购买后可使用者 |
| --- | --- | --- | --- |
| Claude Code 独享账号 A | `claude-a` | `kind: claude` | 购买它的商户 A |
| Codex 独享账号 A | `codex-a` | `kind: codex` | 购买它的商户 B |

商户 A 不会因此获得 `codex-a`，商户 B 也不能使用 `claude-a`。同一商户也可以分别购买两种账号；购买一个账号不会自动独占整个 Agent。账号的识别不依赖名字包含 `claude` 或 `codex`，以上 ID 只是便于识别的示例。

Agent 默认不共享给普通租户，由运维在添加、审批或共享设置中控制。非共享状态仍允许购买者使用对应的有效独享账号，详见[Agent 共享设置](agent-sharing.md)。

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

在运维计费页的「账号包月」点击「添加账号」，先选择 `agent-one`，再分别选择 `Claude Code · claude-a`、`Codex · codex-a`，各自填写币种、月价和 Dodo 产品 ID。选择器使用 Agent 上报的真实类型，只列出 Claude Code / Codex，排除已有商品和商户自有节点；不会把其他运行时当作 CLI 账号。实际 Agent ID 以节点清单为准，若配置了 `gateway.allowed_runtimes`，须同时允许这两个运行时 ID。CLI 登录、模型能力与账号额度沿用各自的适配器。

Claude Code / Codex 是本地账号来源；Dodo Payments 是收款通道，不提供这些账号。售卖账号需要在本机 `deploy/controlplane.yaml` 同时开启账号库存与在线支付：

```yaml
AccountSales:
  Enabled: true
Billing:
  Enabled: true
  ReturnURL: https://<平台域名>/console/billing
  Dodo:
    Environment: live
```

`AccountSales.Enabled` 开启库存、商户列表与账号授权；**购买一个月必须开启 `Billing.Enabled` 并配置 Dodo**（`AISW_DODO_API_KEY`、`AISW_DODO_WEBHOOK_SECRET`，webhook 指向 `<控制面公网地址>/billing/v1/dodo/webhook`），否则结账端点不挂载，Console 提示「在线支付未开启」。`Billing.Enabled` 也会开启点数充值与按 token 扣费，规则见 ControlPlane STATUS「计费与充值」：此后自助注册的商户为 prepaid，存量商户与 BootstrapToken 创建的商户仍为 exempt；商户买到账号后自动切换为 `account_monthly`，不按 token 扣费。配置变更后需重新启动控制面。已有 Docker 部署可用 `bash deploy/upgrade.sh --no-pull controlplane console` 备份并重建这两个服务；首次启用须遵守下文先升级 Gateway 的顺序。

在 Dodo 控制台为账号准备一个**「按意愿付费」（Pay What You Want）产品**，币种与商品一致，最低价不高于月价；控制面在结账时把月价作为金额传给它，多个账号商品可共用同一个产品。不要使用固定价格产品，也不要允许折扣码：付款金额低于订单金额或币种不同时不会开通。

## 使用

1. 在 Agent 上为每个实际账号声明独立的运行时 ID 和 `account_dir`，先完成对应 CLI 登录，见 [Agent 多账号配置](../service/aiServeWeaveAgent/README.md#同一-cli-的多个账户)。不要把同一实际登录通过其他运行时或其他 Agent 同时提供给共享池；平台不读取凭据，无法识别这些登录是否相同。
2. 平台运维进入 `/operator/billing` 的「账号包月」，选择 Agent、选择其上报的 Claude Code / Codex 账号，填写名称、币种（ISO 4217 三位大写，如 `USD`）、月价（以币种主单位输入，如 `19.99`）和 Dodo 产品 ID 并上架。同一 `(node_id, runtime_id)` 只能有一个商品，保存后不能更换目标，只能调整名称、价格、币种、产品或上架状态。上架时该目标立即成为受管库存，即使无人购买也不会向普通商户开放。
3. 商户 owner/admin 在 `/console/billing` 点「购买一个月」或「续费一个月」，确认框显示法币金额与计费模式变化，确认后跳转 Dodo 结账页付款。不扣点数，也不创建自动续费订阅。
4. Dodo 的付款 webhook 验签后，控制面才开通这一个月：商户计费模式自动变为 `account_monthly`，只能调用自己已购且未到期的账号，使用期间不按 token 扣点数。回到 Console 后页面等待服务端确认，从不依据回跳参数判定到账。运维也可在商户计费列表切换此模式。已有模型授权、节点专属绑定和配额继续生效。
5. 「续费一个月」在付款确认时从 `max(当前时间, 本商户原到期时间)` 延长一个 UTC 自然月。目标月没有对应日期时取其最后一天，例如 1 月 31 日 → 2 月 28 日 → 3 月 28 日。每次只买一个月，累计到期时间最多提前十年。到期后可手动续费；其他商户也可购买已经到期的账号。

**结账占用**：商户发起结账后，这笔待付款订单在 30 分钟内为该商户保留空闲账号，其他商户此时结账返回 `409`；Dodo 结账未能发起的订单立即释放占用。超过 30 分钟才完成的付款，账号仍空闲（或仍属于本商户）时照常开通；若账号已被他人买走，订单记为已付款但**未开通**，控制面记录 `mismatch` 错误日志，订单列表显示「账号未开通，待退款」，由运维在订单上发起退款。

**退款**：账号订单没有点数可退，商户自助退款申请会被拒绝；运维可按订单直接退款（`POST /operator/v1/billing/orders/:id/refund`）。退款与争议**不会缩短已开通的期限**，也没有回收期限的接口，退款前请确认是否需要同时下架。

下架阻止新结账和续费，已付款的期限仍有效；下架前已发起、之后才完成付款的结账照常开通。到期或下架不会把账号恢复为共享账号。没有删除库存的接口。到期不终止此前已开始的请求，但不允许新的请求或工具续接；若账号重新售出，原商户此前缓存的到期时间也不能延长其权限。Agent 删除或重命名该运行时不会自动迁移权益，平台运维需保持已售账号的运行时标识和实际登录稳定。

## HTTP 契约

账号、余额概览、商户余额管理、账目和订单读取路由要求 `AccountSales.Enabled` 或 `Billing.Enabled`，两者都关闭时为 `404`；结账路由只在 `Billing.Enabled` 时挂载。租户路由要求租户会话，平台路由要求独立平台会话。旧的点数购买路由 `POST /admin/v1/billing/accounts/:id/purchase` 已移除。

| 路由 | 用途 |
| --- | --- |
| `GET /admin/v1/billing/accounts` | 可购买库存与自己的已购账号，`{items: [...]}`；不暴露其他商户身份或其占用中的账号 |
| `POST /admin/v1/billing/accounts/:id/checkout` | owner/admin 为一个月发起 Dodo 结账，仅 `Billing.Enabled` 时挂载 |
| `GET /operator/v1/billing/accounts` | 全部库存，包括下架、占用和到期账号 |
| `POST /operator/v1/billing/accounts` | 新增库存 |
| `PUT /operator/v1/billing/accounts/:id` | 更新名称、月价、上架状态；目标身份必须与原记录相同 |

创建/更新请求：

```json
{"node_id":"agent-a","runtime_id":"claude-a","name":"Claude 独享账号 A","price_minor":1999,"currency":"USD","dodo_product_id":"pdt_xxx","active":true}
```

`price_minor` 是每月价格的货币最小单位（USD 1999 即 19.99；JPY 没有小数），正整数，上限 `100,000,000`；`currency` 为三位大写字母；`dodo_product_id` 非空，最多 64 字节。库存上限 1000 个。`tnode.` 商户自有节点不允许作为平台售卖库存。Console 从 Agent 清单选择账号，没有上报时提示先接入 Agent；清单读取失败与部分失败分别提示。API 仍允许预先配置库存。上架不证明 CLI 已登录、健康或具备可路由模型，离线账号会标注离线。

结账请求：

```json
{"expected_price_minor":1999,"currency":"USD"}
```

请求中不接受 `tenant_id`、期限、归属等字段。`201` 返回 `{order_id, checkout_url}`，Console 只校验它是绝对 https 地址再跳转。价格或币种与当前标价不同、账号被其他商户持有或正由其他商户结账、续费上限冲突返回 `409`；未上架、尚无法币价格或不可访问 `404`；非法请求 `400`；角色不允许 `403`；联系 Dodo 失败 `503`。结账是写操作，Console 不自动重试；同一个确认框里的人工重试会再开一笔订单，未付款的订单不会开通。

账号响应为 `{id, node_id, runtime_id, name, price_minor, currency, active, owned, expires_at?}`。平台响应可额外带 `tenant_id` 与 `dodo_product_id`；商户响应只用 `owned` 表达本人持有。已到期并重新可售的账号不会向其他商户返回原持有者及其到期时间。`price_minor` 为 `0` 的商品是迁移前按点数定价的旧商品，须重新设定法币价格才能上架。

订单列表（`/admin/v1/billing/orders`、`/operator/v1/billing/orders`）中，账号订单的 `package_id` 为空、带 `account_id`；付款开通后带 `account_expires_at`。已付款却没有 `account_expires_at` 的账号订单表示未能开通，需要退款。

## 数据、授权与升级

`billing/0004_account_sales.sql` 为 PostgreSQL 和 MySQL 新增 `account_sales` 库存、`account_purchases` 收据和 `account_sales_lock` 库存容量锁。`billing/0005_account_fiat.sql` 改为法币定价：`account_sales` 新增 `price_minor`、`currency`、`dodo_product_id` 并删除 `price_micro`，`checkout_orders` 新增 `account_id`、`account_expires_at` 与索引 `idx_checkout_orders_account`。**迁移会把全部现有账号商品设为下架、价格清零**，已售出的商户与到期时间保留；运维须为每个商品填写法币价格与 Dodo 产品 ID 后重新上架。`account_purchases` 与 `account_purchase` 账目保留为历史记录，不再写入。

结账事务锁定账号行，复核标价、持有者和 30 分钟内其他商户的待付款订单，再插入待付款订单。付款 webhook 沿用点数充值的验签与幂等规则（webhook ID 去重、付款 ID 唯一），事务按订单 → 商户计费行 → 账号行 → 吊销 outbox 的顺序加锁，在同一次提交中把订单标为已付款、绑定归属并延长期限、切换计费模式、记录 `account_expires_at` 并递增缓存失效代际。库存创建使用独立容量锁，避免与付款事务倒置加锁。

Key 校验新增 `accounts: {only?, managed?: [{node_id, runtime_id, expires_at}]}`：`only` 表示账号包月模式；`managed` 包含全部受管库存，对不属于该商户的账号下发零到期时间。Gateway 在目录、候选选择、重试派发和固定运行时的工具续接上检查 `(node_id, runtime_id)` 与绝对到期时间。它每次使用缓存授权时仍比较时间；账号包月商户没有有效账号时，新工作返回 `402 account_subscription_expired`。读历史、下载已有产物和取消 Job 仍可使用。控制面的商户模型清单同样过滤账号。

**升级顺序**：先更新全部 Gateway，再对控制面执行 `-migrate up`，更新控制面和 Console，最后为账号填写法币价格并上架。从点数版本升级时，控制面与 Console 须一起更新：旧 Console 仍调用已移除的点数购买路由。旧 Gateway 会忽略新增授权字段，不能承载本功能；上架库存后不要回退到不支持账号授权的 Gateway。Agent 无需为此升级，但多账号运行时声明必须已生效。运行此版本控制面前应完成迁移，启动 schema 校验会检查新表和索引。

## 验证边界

默认测试覆盖法币标价校验、无支付配置时不能结账、结账占用与并发、改价冲突、少付不开通、付款重放、续费从原到期时间延长、自然月月末、下架保留权益、超时后被他人买走的付款不开通、结账失败释放占用、商户不能自助退款而运维可退款、跨租户隔离、节点授权、目录过滤、缓存到期和不向共享账号回退。内存与数据库 store 共用同一套结账场景（`internal/store/storetest`）。Console 契约、白名单、草稿校验、八语言词典及生产构建有独立检查；Chrome 计费专项覆盖法币显示、旧商品不可购买、结账失败后人工重试、未开启在线支付的提示与运维法币编辑。`TestLiveAccountCheckouts` 复用隔离数据库助手，可通过 `AISW_MYSQL_TEST_DSN` 或 `AISW_POSTGRES_TEST_DSN` 运行；测试会创建并删除自己的临时数据库。

此功能尚未在生产环境验收；真实 Dodo 付款、按意愿付费产品配置、实际 CLI 凭据、上游配额和实际部署的缓存通知仍需部署后联调。已购账号自身的上游配额耗尽时不会借用未购买账号。
