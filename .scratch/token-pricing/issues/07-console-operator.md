# 07 Console 运维页

Status: ready-for-agent

Blocked by: 02, 03（effective-prices 需要 06）

规格：[spec.md](../spec.md)「Console / 运维」。

## 范围

模型定价（参照模型搜索、从官方价复制、覆盖价、模型倍率、默认价、价目表同步状态与立即同步）；价目表审核（新旧对照、批准与拒绝、角标）；价格方案；租户列表的方案列与未封账用量；未定价横幅。八种语言。

- 注意（第 4 项之后）：`/operator/v1/billing/model-prices` 已移除，Console 运维计费页的「模型单价」在本项完成前会请求失败；`/api/operator/*` 白名单也要移除这个路径。

## 验收

单元测试、类型检查、lint、生产构建；浏览器检查宽窄屏。
