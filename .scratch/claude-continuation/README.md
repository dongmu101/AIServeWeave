# Claude 长工具链 400 修复

2026-11-04 用户 Agent 日志：`21:27:30+09:00` 首次拒绝为 `continuation_tool_count_mismatch`；随后重试是 `continuation_config_mismatch`，字段为 `beta_features`。不记录请求内容或凭据。

## 已验证的触发与修复

- 旧代码把 `pending` 的全部 MCP 邮箱当成本轮工具集合。旧的已投递结果尚待迟到回调取走时，第二次工具续接会报数量不匹配。改为在每轮响应结束时保存实际公开的完整工具 ID 集合，下一次续接只匹配此集合，旧邮箱继续交给自己的回调消费。
- beta 顺序、空格、重复项按功能集合比较；实际增删 beta 且完整归属、历史、结果集合及其他配置全部匹配时，停止旧 CLI，不向其投递本批结果，在原节点使用新 beta 从完整记录恢复。其他配置、API 版本、归属、历史和部分工具结果仍拒绝。

旧 `580f95d` 源码加入新的 E2E 夹具、固定 beta 列表以隔离变量后，`TestClaudeLongToolContinuation` 在 round 2 返回 HTTP 400 `the request could not be processed`。修复后同一路径通过。

## 验证

```sh
go test ./service/aiServeWeaveAgent/claudecode -count=1 -timeout=60s
go test ./service/aiServeWeaveGateway/e2e -run '^TestClaudeLongToolContinuation$' -count=1 -timeout=60s
go test -race ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/e2e -count=1 -timeout=180s
go test -race ./service/aiServeWeaveAgent/claudecode -run '^TestContinuation' -count=20 -timeout=90s
go vet ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/e2e
git diff --check
```

全部通过。E2E 是真实 mTLS 隧道和实际 MCP HTTP 回调，CLI/模型为可控夹具。真实 Opus 长任务和线上部署尚未验收。

## Mac mini 的已知基线

`fix-after-diagnostics.patch` 适用于用户已报告的 `0a03181` 加上一轮诊断补丁。已在独立目录重建该基线，验证 `git apply --check`、应用修复、Claude 包完整 race 测试与 vet 通过。补丁只包含 Agent Claude 源码及测试，不引入后续附件功能；当前工作区已包含相同修复。

停止原 `go run`，将补丁保存到 Mac mini `/tmp/fix-after-diagnostics.patch`，仓库根目录执行：

```sh
git apply --check /tmp/fix-after-diagnostics.patch
git apply /tmp/fix-after-diagnostics.patch
go test ./service/aiServeWeaveAgent/claudecode -run '^TestContinuation' -count=1
cd service/aiServeWeaveAgent
go run . -config ../../deploy/config.open7x.yaml 2>&1 | tee /tmp/aisw-agent-fixed.log
```

应用检查若失败，保留当前文件并查看错误；不要强行覆盖。运行后从原客户端继续任务验证，Agent 重启后的旧回合走现有历史恢复路径。
