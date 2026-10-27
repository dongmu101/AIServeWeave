# P10 稳定性与界面验收

P10 分别记录熔断校准、逐副本替换与 Console Q03 的证据。合成后端不能校准真实推理负载，同版副本替换不能证明跨版本兼容。版本与发版安排由维护者负责。

真实环境下的长时间稳定性验证由维护者在其自行部署的服务器上进行，不在本仓库的验收流程内维护；`service/aiServeWeaveGateway/e2e/TestSoak` 这一工具仍保留在代码库中（可选的合成后端长稳/回归冒烟，见其源码注释与 `-run '^TestSoak$'`），但不再是 P10 验收的必过项，也不再要求归档 24h 结果到 `docs/acceptance/`。

## 同版逐副本替换

```bash
go test ./service/aiServeWeaveGateway/e2e \
  -run '^TestRollingUpgradeKeepsAtLeastOneTunnelAvailable$' \
  -race -count=5 -v
```

测试逐一停止三个副本、在原地址重建并等待恢复，再处理下一个。每个副本停止期间必须通过其余副本完成 Chat；后台监视器持续发送请求，并计入结束前最后一段成功间隔。测试结束或失败时取消并等待监视器退出。报告标明 `same-source rolling replacement`。

这证明当前源码、本机单进程机群在同版逐个替换时保持服务能力。它不覆盖旧/新二进制混跑、负载均衡摘流、Agent 自身升级或数据库迁移；不据此修改 A03 的兼容承诺。

## 真实流量熔断校准

当前默认仍为连续失败 5 次、初始冷却 5s、最大冷却 2m。此项必须使用明确的真实测试环境与模型；在获得实测结果之前不修改默认值或填写“已校准”。

记录模型与后端版本、节点/副本数量、并发、流式占比、请求期限和可重复的负载条件。分别采集稳定负载、容量饱和、后端超时/断连与恢复阶段的成功率、请求延迟/TTFT、重试、熔断次数与恢复时间；用 `gateway_scheduler_breaker_*`、调度指标与脱敏 request_id 日志交叉核对。背压与限流不计入节点故障，这是当前调度器契约，不因压测而改变。

在相同负载下比较候选参数，记录误熔断与恢复代价，再决定是否调整。报告须给出实测环境、参数、原始指标、结论与适用范围；固定响应、默认单元测试或文档中的历史数字不能充当真实流量结果。

`service/aiServeWeaveGateway/main.go` 提供 `-breaker-failure-threshold`/`-breaker-base-cooldown`/`-breaker-max-cooldown` 三个 flag（默认 `0`，沿用 `scheduler` 包内置默认值），用于在不重新编译的前提下切换候选参数；复现或补充新一轮对比时直接用这三个 flag 起不同的 Gateway 副本。2026-09-13 已用它们在单机真实环境（ControlPlane + Registry + 真实 Agent + 真实 Ollama）下对比了默认值（5/5s/2m）与一组更激进的候选（3/2s/20s），覆盖稳定负载、中度/硬饱和与真实断连恢复四类场景；结论是保留当前默认值，详见 [验收记录](../docs/acceptance/p10-breaker-calibration-2026-09-13/README.md)。该记录仅覆盖单机单节点，多节点机群下的成本收益、以及超过 2 分钟的持续故障窗口仍未验证，后续如需进一步校准可参照该记录「建议」一节列出的缺口。

## Console Q03

使用生产构建、独立回环端口 3100 和临时浏览器上下文。测试签发仅用于自身的合成会话，拦截浏览器 API 调用；上游地址设为不可用的回环端口，未声明请求会令用例失败。它验证页面渲染与交互，不替代 ControlPlane 鉴权、真实数据库或真实业务写入联调。

```bash
cd service/aiServeWeaveConsole
pnpm exec next build --webpack
pnpm exec playwright install chromium
pnpm test:e2e
```

已有本机 Chrome 时可省略浏览器下载，改用 `AISW_BROWSER_CHANNEL=chrome pnpm test:e2e`。`pnpm test` 仍只运行 Node 内置 runner 的纯逻辑测试；Playwright 是独立的开发依赖。标准库没有浏览器驱动与跨进程交互等待能力，引入它是为了直接测量真实 DOM、焦点和浏览器堆，而不是用静态组件样本代替界面。

验收包含：22 个已登录页面在 1440/390/320px 下的布局与表单标签，两个窄屏登录页，已加载的路由/模板编辑器、长名称/ID/节点标签，Tab 顺序与对话框焦点圈定、Escape 关闭/焦点恢复、短屏对话框、错误重试、一次性 Key 关闭后的清理，以及当前导航标记。内部卡片裁切另作断言，不能只看文档宽度。

审计与请求列表分别使用 10,000 条合成记录，保持每页最多 200 条，连续翻阅 50 页，检查末页、键盘滚动与筛选。DOM 数据行上限为 54；第 5/25/50 页用 CDP 触发 GC 后读取堆，第 50 页相对第 5 页增长须小于 8 MiB。该门槛用于发现此次有界分页回归，不等同于浏览器长期无泄漏证明。

结果在 `test-results/results.json`；失败保留截图与 trace，指定窄屏页面另保留成功截图。可用 `--output=../../dist/p10/<run>/browser` 保存附件。覆盖范围是 Chromium 的上述路径，不声称所有浏览器、辅助技术或每个业务弹窗都已验收。
