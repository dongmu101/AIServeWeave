# 04 引导脚本 install.sh（Ubuntu、macOS）

Status: needs-triage
Blocked by: 02, 03

依据：[spec.md「引导脚本」](../spec.md#引导脚本)。

## 范围

- 新增 `deploy/agent/install.sh`：四种凭据方式、三种来源、`--upgrade`、默认账户创建、依赖检查、先下载校验后兑换、临时目录清理。
- `agent.sh` 只做必要改动（如有），保持现有参数与语义。
- `deploy/agent/tests/test_deployment.py` 增加管道执行与桩接口用例。

## 验收

- [ ] spec「Testing Decisions」脚本各项（Ubuntu 与 macOS CI）
- [ ] 输出不含兑换码与令牌
- [ ] `shellcheck`（如可用）无新告警

## Comments
