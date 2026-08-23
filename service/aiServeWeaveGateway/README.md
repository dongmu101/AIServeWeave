# aiserveweave-gateway

数据面。对外终结 OpenAI / Anthropic 兼容 API，对内通过隧道把请求派给节点。

**当前进度：隧道服务端已落地，对外 API 与调度器尚未开始。** 这个二进制现在能接住 Agent 并知道每个节点能服务什么，但还没有把请求送进去的入口。

| 目录 | 状态 | 内容 |
| --- | --- | --- |
| `tunnelserver/` | 已实现 | 隧道终结：mTLS 认证、节点表、槽池、九个 Operation 的分发、`NodeRuntime` |
| `e2e/` | 已实现 | 真实 TCP + mTLS 下三副本与真实 Agent 的联调测试 |
| `main.go` | 部分 | 装配隧道监听；对外 HTTP 监听仍未绑定 |

隧道协议本身定义在 [../aiServeWeaveAgent/tunnel/README.md](../aiServeWeaveAgent/tunnel/README.md)，改这里的代码前先读那份。两端共用 `common/runtime`（类型与接口）与 `common/tunnelwire`（proto 编解码），不允许任一侧另写一份等价转换。

## tunnelserver 的四条约束

这四条不是实现细节，是设计约束，改动时不能绕过：

1. **证书是身份的唯一来源。** `node_id` 只从 TLS 栈**验证过的**证书链里读（`VerifiedChains`，不是 `PeerCertificates`），流上声明的 `node_id` 必须与之相符，不符就断流。没开客户端校验的副本认不出任何人，而不是认可所有人。
2. **不排队。** 没有空闲槽时 `Dispatch` 立刻返回 `ErrorBackpressure`（`Retryable: true`），由调度器换节点。槽是预先 park 好的，所以"这个节点满了"是微秒级的答案。
3. **不缓冲。** 响应帧一帧一交给调用方，调用方不读就阻塞，背压顺着 gRPC 流控传回 Agent。没有队列可以涨，也就没有队列需要限长。
4. **不转发。** 每个副本只服务连到自己身上的节点。请求路径上没有副本间跳转，这是多副本设计的前提，不是优化。

## 下一步

1. **调度器**：按模型与能力从节点表选节点；`code == "backpressure"` 表示换节点且不计入熔断，`Retryable` 表示这个请求能否再跑一次——两者回答的是不同问题。
2. **OpenAI Chat Completions 前门**：普通与 SSE 流式，复用 `common/runtime` 的请求响应类型，不另立一套。
3. **名册来源**：目前 `Server.SetRoster` 由调用方手工注入，上线前要接到 Registry 的名册广播上。
4. **指标**：Agent 侧的 13 个隧道指标已有实现可参照，服务端侧需要对应的一份，标签同样禁止出现 payload 内容。

## 质量门禁

```bash
gofmt -l ./service
go vet ./service/aiServeWeaveGateway/...
go test -race ./service/aiServeWeaveGateway/...
```

`e2e` 包会真的监听回环端口、真的做 TLS 握手、真的跑 Agent 的隧道客户端。它不依赖 GPU、外部网络或真实后端——后端是脚本化的 `runtime.InferenceRuntime`，因为这个包测的是 Agent 与 Gateway 之间发生的事。
