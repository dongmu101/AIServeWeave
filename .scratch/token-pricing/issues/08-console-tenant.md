# 08 Console 租户页

Status: ready-for-human

实现（2026-11-16）：已合并；Console 409 项单元测试、类型检查、lint、生产构建与 Playwright（新增 `usage-pricing.spec.ts`）通过，`billing.spec.ts:382` 为既有失败。

Blocked by: 05, 06

规格：[spec.md](../spec.md)「Console / 租户」「余额账目 / 显示」。

## 范围

用量页的消耗点数、缓存 token 列、费用列与「明细」标签页；API Key 页与用户页的点数；计费页的模型价格分区；账目页的「进行中」一行、用量条目的时间段与请求数及明细链接。八种语言。

## 验收

单元测试、类型检查、lint、生产构建；浏览器检查宽窄屏。
