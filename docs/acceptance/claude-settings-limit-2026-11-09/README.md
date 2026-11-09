# 原生请求的 CLI 设置文件上限修复

日期：2026-11-09。源码基线 `66ad8b3`，本机及用户 Agent 的 Claude Code 均为 `2.1.319`。

## 证据与复现

用户日志在 `2026-11-09T16:17:02+09:00`（台北时间 15:17:02）开始连续失败，路径为 `native`，固定原因 `missing_result`、错误码 `protocol_error`。此前多个原生回合完成；失败回合约 80–234 毫秒结束。该日志没有记录实际请求大小。

真实 CLI 配合回环模拟 API 的 `TestNativeCLIHistorySizeContract` 在旧代码下，128 KiB、512 KiB、1 MiB 历史通过，2 MiB 与 4 MiB 历史均在约 60–80 毫秒失败，固定原因与用户日志一致。进一步用只有合成文字、临时账户目录和不可用回环地址的最小设置文件执行同版 CLI，得到退出码 1、stdout 为空，stderr 为：

```text
Error: Settings file exceeds the 2MiB limit: <temporary settings file>
```

根因是原生 API 文档经 JSON 再次转义后进入 `settings.json` 的 `env.CLAUDE_CODE_EXTRA_BODY`，先撞上 CLI 的设置文件上限，而非平台的 32 MiB 请求上限。这个缺陷可稳定产生与日志相同的失败；真实生产请求大小没有采集，更新后的 `request_bytes` 可用于后续关联，当前不宣称客户机任务已恢复。

## 修复与边界

- 小请求保留既有设置传递；按最终编码的设置字节数判断上限，覆盖引号、反斜杠膨胀。
- 大请求的设置仅保留 beta 选择器，完整文档在本回合内存中经原有每进程代理覆盖 CLI 的主流式 Messages 请求；生成的认证头及未被覆盖的元数据保留，`betas` 不进入 HTTP JSON。
- 仅处理官方 API 的主流式调用，旁路调用不改写。代理识别 gzip 请求，覆盖后更新长度和内容编码；无法识别的种子请求明确失败。
- 原有认证、代理路由、名称受限临时 CA、口令保护和关闭回收规则继续使用；不获取或导出登录令牌，不记录请求内容，不增加公网入口或客户端可配置目标地址。
- 本地缺少结果、进程异常退出按 `upstream_error` 返回；已捕获的上游错误保留原有透传规则。记录原始请求字节数，大小上限仍为 32 MiB。

## 已通过

```sh
go test -race ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/httpapi ./service/aiServeWeaveGateway/e2e -count=1 -timeout=240s
go vet ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/e2e
AISW_CLAUDE_CONTRACT_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestNativeCLI(HistorySizeContract|SafeguardsContract|CompactionContract|RejectionContract)$' -count=1 -timeout=180s -v
git diff --check
```

默认测试覆盖设置边界、转义内容、认证头/元数据保留、gzip、旁路请求及后端错误分类。真实 CLI 契约逐字段检查 2、4、16、31 MiB 历史，4 MiB safeguards 上下文，以及转义密集的 512 KiB 请求；原有 8 轮 safeguards、自动/按需压缩和四类能力拒绝契约亦通过。真实 CLI 的上游为 TLS 回环模拟服务，未使用真实模型额度。

## 尚待验收

用户实际长任务、真实订阅的大历史、Windows/Linux 原生 CLI 及其他 CLI 版本、线上 Agent 和安装包分发尚未验收。本次 Gateway 推理逻辑和隧道协议未改；已有节点需要更新并重启 Agent。平台从 Gateway 镜像分发 Agent 时，另需更新其内置制品，现有节点仍需各自升级。
