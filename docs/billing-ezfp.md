# 易支付（EZFP）人民币收款

[易支付](https://www.ezfp.cn/)（彩虹易支付 V2 协议）是 Dodo 之外的第二个法币收款通道，以人民币收款，租户可选**支付宝**、**微信支付**或 **USDT**（运维在 `Types` 中开启哪几种）。它与 Dodo 服务同样三条结账流程：点数充值包、账号包月（现货）、账号预售。两者可以并存，都可用时由租户在结账时选择；也可以只配易支付。

> 只有**币种为 CNY** 的商品可以走易支付，金额就是商品现有的标价（分），不另设价格。CNY 商品可以不填 Dodo 产品 ID，此时只能经易支付购买；其他币种的商品照旧只能经 Dodo 购买。

到账只认两种经平台公钥验签的来源：易支付的**异步通知**，或控制面主动的**订单查询**。浏览器从易支付回跳 Console 时带的参数（包括 `trade_status`、`sign`）一律不作为到账依据。

## 开通

1. 在易支付商户后台「个人资料 → API 信息」点击「生成商户 RSA 密钥对」，保存**商户私钥**，并复制**平台公钥**与**商户 ID**。
2. 需要系统内退款时，在商户后台打开「订单退款 API 接口」开关。
3. 把两把密钥写入部署环境变量（`deploy/.env`，`docker-compose.yaml` 已透传）。密钥可以是后台显示的单行 base64，也可以是 PEM：

```bash
AISW_EZFP_PRIVATE_KEY=MIIEvQIBADANBgkqhkiG9w0BAQEFAASC...
AISW_EZFP_PUBLIC_KEY=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A...
```

4. 在 `controlplane.yaml` 开启：

```yaml
Billing:
  Enabled: true
  Ezfp:
    Enabled: true
    PID: 1001                       # 商户 ID
    PrivateKey: "${AISW_EZFP_PRIVATE_KEY}"
    PublicKey: "${AISW_EZFP_PUBLIC_KEY}"
    Types: [alipay, wxpay, usdt]    # 可省略，默认 [alipay, wxpay]；usdt 须明确列出
    NotifyURL: https://console.example.com/billing/v1/ezfp/notify
    ReturnURL: https://console.example.com/console/billing
```

`NotifyURL` 与 `ReturnURL` 必须是 HTTPS，且**不能带 query 或 fragment**：易支付会在后面追加自己的参数。只配易支付时可以省略 `Billing.Dodo` 和 `Billing.ReturnURL`，Dodo webhook 与客户门户不挂载。密钥在启动时解析，格式错误会让控制面启动失败。

`Types` 可列 `alipay`、`wxpay`、`usdt`，不可重复。`usdt` 不在默认值里：先在易支付商户后台确认 USDT 通道已开通，再加进 `Types`，否则租户点击后易支付会拒绝下单（控制面返回 `503`）。USDT 订单仍按商品的人民币标价下单，由易支付在付款页换算成 USDT；本服务的订单、入账与退款金额都以人民币计。部分通道不支持退款，USDT 退款被易支付拒绝时按「明确拒绝」处理。

5. 让易支付能投递通知：`deploy/docker-compose.yaml` 把控制面端口只绑定在 `127.0.0.1`，需要反向代理把 `/billing/v1/ezfp/notify` 这一条路径（GET 与 POST）转到控制面，见 `deploy/nginx.example.conf`。经 Cloudflare 代理时，要让 WAF 与 Bot Fight Mode 放行这条路径。通知的 query 里带付款人标识与签名，示例配置对这条路径关闭了访问日志。
6. 控制面所在服务器须开启时间同步：易支付接口答复的时间戳与本机时钟相差超过 5 分钟时会验签失败，结账返回 `503`，恢复也会停滞。

## 使用

- 运维在 `/operator/billing` 建立或编辑商品时选择币种 `CNY`，Dodo 产品 ID 可以留空（只走易支付）；同时填写的话，两种方式都可买。
- 租户 owner/admin 在 `/console/billing` 看到 CNY 商品时，按钮显示「支付宝」「微信支付」（两者都可用时另有「银行卡 / Dodo」）。点击后跳转易支付付款页。其他角色只能查看。
- 付款后易支付把浏览器送回计费页。Console 先清除地址栏中易支付追加的参数，请控制面就这一笔订单查询一次，再照常等待服务端确认。订单列表中待付款的易支付订单有「再次确认」按钮，每按一次查询一次。

## 防重复入账与交付

- **先落库再下单**：订单（`ord_` id 即易支付的 `out_trade_no`）先以 `pending` 保存，点数或开通内容在此时确定，再调用 `api/pay/create`（`method=jump`）。下单失败时订单标为失败，并释放账号占用或预售名额，返回 `503`。原因记在控制面的 `opening an 易支付 payment failed` 日志里：`code` 与 `msg` 非空表示易支付明确拒绝，`msg` 是易支付给商户的原因（截短、去控制字符，不会出现在页面上）；`code=0` 时看 `error`，它是超时、HTTP 状态或验签失败。
- **同一条结算路径**：通知与查询都归约为和 Dodo webhook 相同的付款事件，交给同一个事务。账号订单开通一个月，预售订单进入「待发货」，充值包记入 `topup` 点数。
- **逐项核对**：商户 ID 一致；`trade_status` 为 `TRADE_SUCCESS`（查询时 `status` 为 1）；订单确实是易支付订单；交易号与下单时记录的一致；币种为 CNY；实付金额不低于订单金额；`param` 中的租户与订单一致。任一项不符都不入账，记错误日志，留给运维处理。
- **三道去重**：事件 id `ezfp:pay:<trade_no>`、付款 id `ezfp:<trade_no>` 唯一约束、账目幂等键 `topup:ezfp:<trade_no>`。通知重投、通知与查询同时到达，都只入账一次。
- **通知只验签，不检查时间戳新旧**：结算是幂等的，重放拿不到任何东西；按原时间戳重投的通知若被拒，这笔付款就丢了。时间戳与本机相差超过 10 分钟时记一条警告日志。重复出现的参数或带方括号的参数一律验签失败。
- **两条补救路径**：通知是主路径。Console 回跳与「再次确认」会触发一次查询，同一订单两次查询至少间隔 5 秒。后台每分钟查询一批（最多 20 笔）创建满 30 秒、未满 2 小时的待付款易支付订单。超过 2 小时不再主动查询，迟到的通知仍会正常入账。

## 退款

易支付订单与 Dodo 订单一样，可以由租户在退款期限内申请、运维审批，也可以由运维直接退款。差别在于易支付同步答复：

- 以退款申请的 `rfd_` id 作为 `out_refund_no` 调用 `api/pay/refund`。答复成功时立即结算：记下 `ezfp:<refund_no>`，按退款占实付金额的比例扣回点数（幂等键 `refund:ezfp:<refund_no>`），全额退完订单变为「已退款」。
- 易支付明确拒绝（`code` 非 0）时，申请标为失败，冻结的点数退回。部分通道不支持部分退款，会被拒绝。
- 答复丢失（超时、5xx、验签失败）时保持「处理中」。后台每分钟用 `api/pay/refundquery` 查询：成功则结算，失败则标为失败并退回点数。易支付查不到这笔退款时保持「处理中」，由运维在易支付后台核实后用「标记失败」处理。系统**不会**自动用同一个 `out_refund_no` 重发，因为无法区分「退款已完成」与「从未收到」。
- 账号与预售订单的退款规则不变：退款不缩短已开通的期限。

## 接口

见 [ControlPlane STATUS「计费与充值 → 易支付（EZFP）」](../service/aiServeWeaveControlPlane/STATUS.md#易支付ezfp)。迁移为 `billing/0009_ezfp.sql`，给 `checkout_orders` 增加 `provider` 与 `pay_type` 列。控制面与 Console 须一起升级，Gateway 与 Agent 不需要升级。

## 升级与回退

1. 备份数据库，执行 `-migrate up`。0009 只增加带默认值的列，旧版程序照常运行。
2. 一起更新控制面与 Console。旧 Console 不认识易支付：开启易支付后，它会把不带 Dodo 产品的 CNY 充值包当作 Dodo 商品显示，点击后返回「在线支付未开启」。
3. 配置密钥、`Billing.Ezfp`、反向代理与商户后台，重启控制面。
4. **不要在有易支付订单后回退控制面**：旧版会把易支付订单当作 Dodo 订单，退款会发往 Dodo 并失败。
5. 关闭易支付后，通知路由返回 `404`，易支付会重试直到放弃；仍有待付款订单时不要关闭。

## 验证边界

已用模拟的易支付服务验证（`internal/billing/ezfp`、logic、handler、e2e）：

- 签名内容与算法与 OpenSSL 生成的固定签名向量一致，对应 PHP SDK 的 `openssl_sign`。
- 被篡改、未签名、时间戳过期、属于其他商户的答复都会被拒绝；非 2xx、非 JSON、超大答复与重定向一律视为结果未知。
- 三种结账、通知的验签与重放、金额不足、`param` 不符、Dodo 事件不能结算易支付订单。
- 查询补救、恢复窗口，以及退款的成功、拒绝、答复丢失后的恢复。
- e2e 用真实客户端，经 HTTPS 连到模拟易支付，走完「只配易支付 → 建 CNY 充值包 → 结账 → 签名 GET 通知 → 入账 → 重放 → 运维退款」。
- 临时 PostgreSQL 16 与 MySQL 8.0 上跑过 0009 迁移，以及共享场景 `storetest.EzfpOrders`（入账、去重、退款关联、旧订单兼容、无产品 CNY 商品只能走易支付）。

**未验证**：

- 没有用真实商户号联调，以下行为尚未实测：手机微信 jump 页面的表现；`out_refund_no` 是否真的防重复；通知重投时是否重新签名；最低金额。
- `clientip` 填的是控制面看到的请求来源，即 Console 的地址，不是付款人的真实 IP（Console 不转发用户 IP）。电脑扫码不受影响，微信手机 H5 支付可能因此受限。
- 在易支付后台手工发起的退款不会同步进系统。
- 多副本部署时每个副本都会运行恢复。结果幂等，但查询量随副本数增加。
- 生产部署。
