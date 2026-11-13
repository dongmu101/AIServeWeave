# 商户订单删除（2026-11-13）

商户计费页「订单」新增删除与确认框，owner/admin 可删除任意状态的本商户订单；member 只读。删除持久化为 `checkout_orders.tenant_deleted_at`，仅商户列表隐藏，运维历史、付款恢复、去重、发货、退款与库存占用继续使用原订单。新接口为 `DELETE /admin/v1/billing/orders/:id`，成功 `204`，跨商户、未知与已删除统一 `404`，审计动作 `billing.order.delete`。Console 白名单、八语言提示、失败保留确认框与末页回退已接入。迁移为 PostgreSQL/MySQL `billing/0010_order_deletion.sql`，`sql/init.sql` 已同步；控制面与 Console 须一起更新。行为与升级说明见[商户订单删除](docs/billing-orders.md)。

验证：控制面默认测试及 logic/handler/store 竞态检查、Console 375 项单元测试、lint（仅既有 alerts 页警告）、类型检查与生产构建、Chrome 计费回归 29 项通过。新增共享 store 场景覆盖所有订单状态、租户隔离、分页前过滤、删除后的迟到付款与重放去重；新增 HTTP/logic 用例覆盖授权与审计。真实 PostgreSQL/MySQL 迁移、真实付款与生产部署尚未验收。

# 易支付（EZFP）人民币收款（2026-11-12）

新增易支付（彩虹易支付 V2）作为 Dodo 之外的第二个法币收款通道，租户可选支付宝或微信支付，用于充值包、账号包月与账号预售三条结账流程。只有币种为 CNY 的商品可以走易支付，金额就是现有标价，不另设价格；CNY 商品可以不填 Dodo 产品 ID，此时只能经易支付购买。易支付可与 Dodo 并存（结账时由租户选择），也可单独开启计费。到账只认经平台公钥验签的异步通知或控制面主动查询，两者都走 Dodo 已有的入账事务与去重；Console 回跳只触发一次查询，订单列表另有「再次确认」，后台每分钟补查 2 小时内的待付款订单。退款以退款申请 id 为 `out_refund_no` 同步结算；答复丢失时以退款查询恢复，不自动重发。配置为控制面 `Billing.Ezfp`，密钥经 `AISW_EZFP_PRIVATE_KEY` / `AISW_EZFP_PUBLIC_KEY` 注入，反向代理须暴露 `/billing/v1/ezfp/notify`（已更新 `deploy/` 下的配置示例、`.env.example`、Compose 与 nginx 示例）。迁移为 `billing/0009_ezfp.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则、开通步骤与验证边界见[易支付说明](docs/billing-ezfp.md)。

已通过的检查：控制面默认测试，billing、logic、handler、store、config、e2e 的竞态检查；签名内容与算法与 OpenSSL 生成的固定向量一致；e2e 以真实客户端经 HTTPS 连模拟易支付，跑完「只配易支付 → 建 CNY 充值包 → 结账 → 签名 GET 通知入账 → 重放不重复 → 运维退款」；临时 PostgreSQL 16 与 MySQL 8.0 上的 gormstore live 用例（含 0009 迁移与 `storetest.EzfpOrders`）。Console 375 项单元测试、lint（仅 1 个既有警告）、TS7 类型检查与生产构建通过，本机 Chrome 计费专项 26 项通过；全套浏览器用例中 `console-header.spec.ts` 两项、`console.spec.ts` 三项（320px 的 `/operator/routes`、`/operator/workflow-templates` 与导航 `aria-current`）失败，已在未含本次改动的 HEAD 上复现同样 5 项，与本次无关。未用真实商户号联调（手机微信 jump、`out_refund_no` 防重复、通知重投是否重新签名均未实测），`clientip` 是 Console 的地址而不是付款人真实 IP，尚未部署。

# 公开首页与法律条款（2026-11-12）

为配合 Antom MOR 审核（要求网站公开展示产品、价格、T&C、隐私条款与退款条款），Console 新增公开页面：`/` 未登录时改为产品介绍首页（已登录照旧进入概览），新增 `/terms`、`/privacy`、`/refund`（英文、简中、繁中，以英文为准，其余语言显示英文），首页、价格、登录、注册页加统一页脚，注册页加同意声明，价格页加分区说明，计费页加条款链接。退款条款按现有规则撰写（点数在 `RefundWindowDays` 内退未用部分，经邮件申请；已开通包月不退；预售未交付全额退；支付宝额度不可撤销）。营运方名称、地址、邮箱、准据法与退款天数来自 Console 环境变量 `AISW_CONSOLE_LEGAL_*`（已接入 Compose 与 `.env.example`），未设置时显示方括号占位符。只需更新 Console，控制面、Gateway 与 Agent 无需升级。说明见 [Console README](service/aiServeWeaveConsole/README.md#公开首页与法律条款)。

同时修复 `app/globals.css` 中 `--font-sans` 引用自身的问题（原本整个 Console 实际显示为浏览器默认的 Times 字体，现恢复为已载入的 Geist），这会影响所有页面的西文字形。

已通过：Console 362 项单元测试（含新增 8 项法律页测试与八语言词典检查）、lint、TS7 类型检查与生产构建；本机生产模式下以模拟目录检查 7 个公开页 × 4 种宽度（1440/768/390/320）× 4 种语言（繁中、英、日、德），均无页面横向卷动或元素超出视窗，页首、正文、页脚左右对齐，页脚链接点击高度不小于 24px。条款正文未经法律审阅，营运方资讯仍为占位符，尚未部署，也未提交 Antom 审核。

# 支付宝（一人付）点数兑换（2026-11-11）

新增支付宝付款方式：商户在支付宝购买次数或积分套餐，额度留在支付宝，在 Console 计费页「支付宝」分区兑换为点数。控制面先保存兑换（编号即一人付幂等键），只有一人付确认核销成功且编号、数量、单位一致时，才在同一事务里记入 `alipay_topup` 点数；结果未知时以原编号查询或原参数重发，后台每分钟恢复，绝不换号再扣。购买入口、回跳和扫码都不入账。Dodo 改为可选，只配支付宝也能开启点数计费（充值包、结账、门户与退款随 Dodo 关闭）。API Key 只从控制面环境变量 `opcpay_api_key` 读取。迁移为 `billing/0007_alipay.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则、开通步骤与验证边界见[支付宝说明](docs/billing-alipay.md)。

已通过的检查：控制面默认测试与 opcpay 客户端、logic、handler、store 的竞态检查；临时 PostgreSQL 16 与 MySQL 8.0 上的 gormstore live 用例（迁移、schema 检查、并发结算只入账一次、并发创建只留一笔待确认）。Console 347 项单元测试、lint、TS7 类型检查与生产构建通过，本机 Chrome 计费专项 17 项通过。未配置真实 `opcpay_api_key`，没有只读查询真实能力与套餐，也没有真实支付宝购买、核销和部署验收；只支持次数/积分套餐，一人付通知与退款未接入。

# 账号预售（2026-11-11）

新增账号预售：运维只定义类型（Claude Code / Codex）、法币月价、Dodo 产品 ID 和名额上限，不绑定具体账号。商户经 Dodo 付款后，订单进入「已付款，待发货」，此时不开通账号，也不切换计费模式。运维在 `/operator/billing` 的「账号预售 → 待发货订单」为每笔订单分配一个同类型的空闲库存账号，分配时开通一个自然月并切换为账号包月；无法交付时可以在同一列表全额退款。账号商品新增 `kind`（创建时必填），下架商品可以不标价，仅供预售分配。迁移为 `billing/0006_account_presale.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则与契约见[账号预售](docs/billing-accounts.md#账号预售)。

**支付宝额度支付（2026-11-11 新增）**：运维可在预售上设置「支付宝额度数量」（0 或留空表示不走支付宝），商户在 Console「账号购买」用支付宝额度支付一个月。控制面先保存待支付订单与待确认核销（`action_id` 幂等），只有一人付确认核销成功才把订单标为已付款并进入「待发货」；确定失败则订单失败并释放名额；结果未知保持待确认并由原恢复流程续办。支付宝订单**系统内不可退款**（一人付没有核销撤销），由运维线下处理。次数包每次核销只能扣 1，数量大于 1 的预售需要积分包。迁移 `billing/0008_alipay_presale.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。未用真实 `opcpay_api_key` 验收，gormstore 的预售支付宝路径只由共享规则与默认测试覆盖，未在临时 PostgreSQL/MySQL 上跑 live 用例。

已通过的检查：控制面默认测试，store、logic、handler 与 e2e 的竞态检查；在临时 PostgreSQL 16 与 MySQL 8.0 上运行的 gormstore live 用例（迁移、升级保留数据、账号结账、预售）。Console 侧通过 338 项单元测试、lint、TS7 类型检查与生产构建，本机 Chrome 计费专项 15 项全部通过。`console.spec.ts` 中有 1 项与本次改动无关的导航用例失败。真实 Dodo 付款、本地与生产部署、真实 CLI 账号的发货联调尚未验收。

# 账号包月改为法币标价（2026-11-11）

账号包月商品改为以法币标价（币种 + 月价 + Dodo 产品 ID），商户经 Dodo 在线结账购买或续费一个月，不再使用点数余额；点数购买路由已移除。付款 webhook 验签后才开通期限，待付款结账为本商户保留账号 30 分钟，超时后被他人买走的付款记为「未开通，待退款」。迁移 `billing/0005_account_fiat.sql` 会把现有商品下架并清零价格（已售期限保留），运维须重新定价上架。购买需要 `Billing.Enabled` 并配置 Dodo，见[账号包月说明](docs/billing-accounts.md)。

控制面默认测试与竞态检查、Console 328 项单元测试、lint、TS7 类型检查、生产构建和 Chrome 计费专项 12 项通过；临时 MySQL 8.0 实测迁移（含旧数据升级）与结账/付款事务通过。PostgreSQL live、真实 Dodo 付款、本地与生产部署尚未验收。

# Agent 默认不共享与运维控制（2026-11-09）

机群页现可直接对每个平台注册节点确认开启或停止共享（2026-11-10）。新增 Agent 接入/审批共享选项与共享设置，默认不共享；未配置的 Agent 也不进入普通租户的模型目录和调度池。有效的独享账号购买权益与专属租户绑定保留。基础迁移 `0015_node_sharing.sql`、授权载荷和 Gateway 检查已实现，使用及升级顺序见[Agent 共享设置](docs/agent-sharing.md)。

控制面与 Gateway 相关默认/竞态测试、326 项 Console 单元测试、共享操作与运维宽窄屏 Chrome 检查通过；隔离 MySQL 的迁移、模式持久化与账号购买回归通过。本地 Gateway、控制面和 Console 已备份后重建，真实运维/租户登录验证：1 台 Agent 已接入、无显式共享，租户模型目录不包含其服务；共享设置 API 返回 200，租户写入被拒绝。PostgreSQL live、生产升级和真实 CLI 账号联调尚未验收。


## Agent 账号独享包月（2026-11-09）

已交付 Claude Code 与 Codex 账号商品、点数余额购买/续费、独享授权与 Console 入口。同一个 Agent 中的两种账号可分别售给不同商户。账号以 Agent ID + 运行时 ID 定位，按 UTC 自然月延长期限；购买后切换为账号包月模式，仅可调用自己已购且未到期的账号。接口、迁移和升级顺序见 [账号包月说明](docs/billing-accounts.md)。

账号包月现支持独立开关 `AccountSales.Enabled`，不要求配置 Dodo。新增表单改为选择 Agent 与实际上报的 Claude Code / Codex 账号，排除重复商品，并支持刷新清单与失败重试。本地 Docker 的控制面和 Console 已重建、启用独立开关，真实运维登录后账号 API 返回 200；Agent 接入后的默认共享准入见上节，真实 CLI 账号售卖联调尚未验收。

两种账号同 Agent 分别售卖的 logic 测试和真实隧道加模拟后端的 scheduler 测试（Claude 原生 Messages / Codex Chat）已通过竞态检查；两种真实上游登录的售卖联调仍待验收。

默认测试、相关竞态检查、Console 类型检查与生产构建通过；Chrome 计费专项 11 项和隔离 MySQL 9.7 账号事务实测通过。PostgreSQL live 测试未运行，生产升级、真实付款及实际 CLI 账号联调尚未验收。
