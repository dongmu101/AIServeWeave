
## Agent 账号独享包月（2026-11-09）

已交付 Claude Code 与 Codex 账号商品、点数余额购买/续费、独享授权与 Console 入口。同一个 Agent 中的两种账号可分别售给不同商户。账号以 Agent ID + 运行时 ID 定位，按 UTC 自然月延长期限；购买后切换为账号包月模式，仅可调用自己已购且未到期的账号。接口、迁移和升级顺序见 [账号包月说明](docs/billing-accounts.md)。

两种账号同 Agent 分别售卖的 logic 测试和真实隧道加模拟后端的 scheduler 测试（Claude 原生 Messages / Codex Chat）已通过竞态检查；两种真实上游登录的售卖联调仍待验收。

默认测试、相关竞态检查、Console 类型检查与生产构建通过；Chrome 计费专项 8 项和隔离 MySQL 9.7 账号事务实测通过。PostgreSQL live 测试未运行，生产升级、真实付款及实际 CLI 账号联调尚未验收。
