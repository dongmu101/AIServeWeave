# 06 Console：「添加节点」一行命令对话框

Status: ready-for-agent
Blocked by: 01

依据：[spec.md「Console」](../spec.md#console)。

## 范围

- 运维「Agent 接入」与商户「自有节点」：系统 / 接入方式 / 来源选择，命令拼装与复制，到期时间；ZIP 入口收进「高级」。
- `upstream-routes.ts` 两条写路由；`lib/console/` 契约镜像；i18n 各语言文案。
- 只发一次、不自动重试、离开不可找回；同源校验与有界请求体沿用现有写路由。

## 验收

- [x] Console AGENTS 的 lint、build、typecheck、test（本机无 pnpm，直接用 `node_modules/.bin` 的 eslint、next build、tsc；312 个单测通过，lint 仅 1 条既有警告）
- [x] 命令拼装覆盖三系统 × 三方式 × 两来源（`agent-install.test.ts`）

## Comments

### 完成记录

- 新增 `lib/console/agent-install.ts`（契约解析 + 命令拼装）与测试；`upstream-routes.ts` 两行白名单；共用组件 `components/console/agent-install-panel.tsx`；运维 `/operator/agent-enrollment` 与租户 `/console/nodes` 接入，ZIP 表单收进「高级」折叠区；八语言文案（`install.*` 与 `operator.enrollmentAddresses`，在各语言 `operator.ts`）。
- 与 spec 的差异：用页面内面板而非弹窗，沿用现有页面结构；运维页把 Registry/Gateway 地址输入提到面板之上，一行命令与 ZIP 共用。
- 创建后租户列表会刷新（码签发已占用一个节点名额）。
- 未验证：浏览器里的实际渲染与剪贴板复制（未跑 Playwright 与真实控制面）；八语言译文未经母语审校；e2e 与 i18n e2e 未执行。
