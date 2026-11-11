# 支付宝（一人付）点数兑换

商户（租户）可以在支付宝购买**次数或积分套餐**，再在 Console 计费页把额度**兑换为本平台点数**，之后用量照旧从点数余额扣减。收款由[一人付（opcpay）](https://ai6666.com/opcpay/)对接支付宝。支付宝可以与 Dodo 并存，也可以单独开启。

> 额度保存在支付宝，点数保存在本平台。一人付不报告逐笔付款，也不提供退款，因此购买入口、页面回跳、扫码都**不会**入账。只有控制面确认成功的核销才会记入点数。

## 开通

1. 在[一人付官网](https://ai6666.com/opcpay/)创建项目，网址填写 Console 的对外地址。
2. 按官网第 2 步扫码进入支付宝（「授权信息」显示「授权平台：碳基圈」），设置收费方式。**只支持次数（COUNT）或积分（POINT）的数量套餐**；买断与时长套餐会在页面上显示为「无法兑换点数」。
3. 在项目控制台申请 API Key，写入部署环境变量 `opcpay_api_key`（`deploy/.env`，`docker-compose.yaml` 已透传）。Key 只从环境变量读取，不写进配置文件、日志或前端。
4. 在 `controlplane.yaml` 开启：

```yaml
Billing:
  Enabled: true
  Alipay:
    Enabled: true
    CreditsPerUnitMicro: 1000000   # 每 1 次 / 1 积分兑换 1 点（1 点 = 1,000,000 micro）
    ReturnURL: https://console.example.com/console/billing?tab=alipay&alipay_return=1
```

`ReturnURL` 可以不填。填写时必须是 HTTPS，主机与端口要与一人付项目登记的网址一致。只配支付宝时可以省略 `Billing.Dodo` 和 `Billing.ReturnURL`，此时充值包、Dodo 结账、客户门户与退款都不挂载。模型单价仍在运维计费页的「模型单价」中设置。

开通后可以先只读核对实际能力与套餐，不会产生付款或核销：

```bash
# 在任意临时目录执行；opcpay_api_key 已在当前 shell 的环境中，勿写进命令历史
npm install --no-save opcpay@0.3.4
node node_modules/opcpay/index.mjs capabilities
node node_modules/opcpay/index.mjs product
```

## 使用

计费页「支付宝」分区显示：

- 支付宝剩余额度与兑换比例。
- 支付宝中配置的套餐，价格以支付宝为准，点「用支付宝购买」跳转到支付宝购买入口。
- 兑换表单：积分套餐输入数量，次数套餐每次兑换 1 次。额度不足时会提示先购买。
- 待确认兑换（可以「重新确认」）与兑换记录。

只有 owner 与 admin 可以购买或兑换，其他角色只能查看。

## 防重复扣减与交付

- **用户身份**：一人付的 `user_id` 是会话中的租户 id，不从请求读取。请求体只接受 `action_id`、`sku_id`、`amount`，其他字段一律返回 `400`。
- **先落库再调用**：兑换先保存为 `pending`，其 `apr_` id 就是一人付的 `idempotency_key`，数量与点数在保存时确定。购买意图同样先保存，`apu_` id 即编号。
- **只认确认的成功**：`SUCCEEDED` + `confirmed=true`，且编号、数量、单位都一致时，才在**同一事务**里把记录标为成功，并以 `alipay:<id>` 为幂等键记入 `alipay_topup` 账目。并发或重复的结算只入账一次。
- **结果未知不换号**：`PROCESSING`、`UNKNOWN`、超时、5xx 都保持 `pending`。之后用原编号查询 `/consumption`；查不到（`404`）时，用原参数重发 `/consume`。
- **一次一笔**：每个租户同时只能有一笔待确认兑换。浏览器在答复丢失后再按一次会沿用同一个 `action_id`，控制面会恢复原兑换，而不是新建一笔。
- **重启恢复**：后台每分钟恢复一批创建满 30 秒的待确认兑换。成功但字段不符的答复会记错误日志并保持待确认，留给运维处理。
- **不把未开放能力当作可用**：一人付项目未 `ENABLED`、收费模式不是数量套餐、能力表未提供时，页面不提供对应按钮，接口返回 `409`，不会调用一人付。

## 接口

见 [ControlPlane STATUS「计费与充值 → 支付宝（一人付）」](../service/aiServeWeaveControlPlane/STATUS.md#支付宝一人付)。迁移为 `billing/0007_alipay.sql`。控制面与 Console 须一起升级，Gateway 与 Agent 不需要升级。

## 验证边界

已用模拟的一人付接口验证：身份与参数伪造、重复点击、核销成功后答复丢失、`UNKNOWN` 后重发、恢复期间一人付不可用、成功字段不符、额度不足、不支持的收费模式。临时 PostgreSQL 16 与 MySQL 8.0 上的 live 用例也已通过，覆盖迁移、schema 检查、并发结算与并发创建。

**未验证**：真实 `opcpay_api_key` 的只读查询、真实支付宝购买与核销、生产部署。一人付通知（webhook）没有接入，到账靠读取额度。一人付没有核销撤销和退款，已兑换的点数如需退回，只能由运维手工调整余额，支付宝侧的额度不会返还。
