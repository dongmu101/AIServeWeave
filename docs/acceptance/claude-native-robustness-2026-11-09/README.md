# Claude 原生链路后续加固

日期：2026-11-09。基线 `81d1a13`，接续[设置文件上限修复](../claude-settings-limit-2026-11-09/README.md)。以下是代码审查后以离线测试复现的额外缺陷，不表示用户原任务曾触发它们。

## 复现与修复

| 触发条件 | 旧行为 | 修复后与回归 |
| --- | --- | --- |
| HTTP/HTTPS 父代理接收 CONNECT 后不应答，Agent 关闭本回合代理 | 已拨号 socket 尚未进入连接跟踪，取消不能打断读取，只能等待剩余 30 秒期限 | 取消关闭握手 socket；关闭测试在 1 秒内完成。成功建立的隧道在拨号上下文释放后仍能收发，保留缓冲字节 |
| SSE `event:error` 用多行 `data` 表示 JSON，或 event 字段位于 data 之后 | 第一行就被当成完整错误，JSON 校验失败后不再捕获，丢失上游错误信息 | 在空行边界合并 data，支持 LF/CRLF/CR；429、529 与 Retry-After 进入既有透传链路；原始流字节不变 |
| SSE 事件被截断、event 名随后被替换、累计数据超过 64 KiB | 可能提前误捕获或使后续合法错误丢失 | 只处理完整 error 事件，按总数据大小限界；超限事件后仍能捕获下一合法错误 |
| CLI 使用 `Content-Encoding: GZip` 发送主请求 | 大请求补入路径把合法压缩种子当成不可读，返回 502 | 编码名按大小写无关识别，与 `gzip` 行为一致 |
| Agent 生成空闲或会话寿命计时器到期，客户端仍连接 | 取消原因退化为 `context.Canceled`，最后公开 `protocol_error`（400） | 使用取消原因保留 DeadlineExceeded，记录 `deadline_exceeded`，公开 `timeout`（正文前 HTTP 504）；不改变时限或自动重试策略 |

SSE 分帧依据 [WHATWG 事件流解释规则](https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation)。逐字节输入测试覆盖分片，不增加读取等待或重写下游流。输入文档仍受 32 MiB 限制，CLI 元数据合并后的代理保护上限为 64 MiB，文档已区分这两处边界。

## 验证

所有新增缺陷均先以回归测试在旧实现下失败，再修改实现。默认测试使用回环 HTTP/TLS 服务及注入时钟，不访问真实 API、不使用登录令牌或模型额度。测试同时检查取消回收、成功连接、流字节保留、错误分类和重试信息。

以下检查通过：

```sh
go test ./... -count=1 -timeout=240s
go vet ./...
go build ./...
go test -race ./service/... -count=1 -timeout=240s
go test -race ./service/aiServeWeaveAgent/claudecode -run '^TestUpstreamProxy' -count=25 -timeout=90s
AISW_CLAUDE_CONTRACT_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestNativeCLIHistorySizeContract$/^32505856$' -count=1 -timeout=90s -v
AISW_CLAUDE_CONTRACT_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestNativeCLISafeguardsContract$' -count=1 -timeout=90s -v
gofmt -l ./service ./api
git diff --check
```

真实 CLI `2.1.319` 加回环模拟 API 复验通过 31 MiB 历史，以及原有 8 轮原生契约（工具 ID、思考、缓存、协议选择器与调用方压缩）。CONNECT 回收与成功隧道回归在 race 下重复 25 次通过。规范与需求两路独立审查均未发现新增阻断缺陷。

另执行 `go generate ./api/...`，再生成差异仅为本机 protoc `7.35.1` 与仓库 `7.34.1` 的版本注释；已恢复生成文件，未改动协议或生成逻辑。

## 尚待验收

真实生产长任务、真实父代理环境及其他系统尚未验证。本次不修改 Gateway 推理逻辑或隧道协议；现有节点需要更新并重启 Agent。平台从 Gateway 镜像分发 Agent 时，另需更新其内置制品，现有节点仍需各自升级。
