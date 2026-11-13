# 商户订单删除

商户在 `/console/billing?tab=orders` 的每笔订单上点击「删除」，核对订单编号并确认后，该订单会从本商户的订单列表移除，刷新或重新登录后也不再显示。仅 owner/admin 可以删除，member 仍可查看订单。所有订单状态均支持删除。

删除只清理列表，不会取消支付、释放待付款账号或预售名额、发起退款、扣回点数或改变账号权益。已经打开的结账仍可能完成付款；异步通知和后台复核继续处理。后台保留交易记录，运维仍可查询、发货和退款。确认框会说明这些行为；请求失败时保留确认框，写入不自动重试。

## 接口

`DELETE /admin/v1/billing/orders/:id`：需要租户会话，租户范围取自会话。无请求体，成功返回 `204`；member 返回 `403`；未知、跨租户或已经删除的订单均返回 `404`。同时记录 `billing.order.delete` 审计，目标为订单编号。没有运维删除接口。

`GET /admin/v1/billing/orders` 在筛选与游标分页之前排除已删除订单；`GET /operator/v1/billing/orders`（含 `fulfillment=awaiting`）、付款恢复、退款与库存占用计算仍包含这些订单。持久化字段是普通可空时间 `checkout_orders.tenant_deleted_at`，并非 GORM 全局软删除字段，避免付款路径误排除订单。

## 升级

控制面与 Console 一起更新。先执行标准数据库升级流程，应用 PostgreSQL/MySQL 的 `billing/0010_order_deletion.sql`，再启动新控制面和 Console；迁移只增加可空列，旧订单继续显示。Gateway、Agent 无需升级。数据库升级与备份步骤见[数据库升级与恢复](../deploy/database-recovery.md)。
