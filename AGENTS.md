# AIServeWeave 开发约定

分布式 AI 推理节点管理平台，Go 1.27。按开源项目标准开发：文档、测试、注释与代码同等重要。

## 先读哪份文档

本文件只写跨仓库的通用约定，细节以专门文档为准，不在此复述：

- [README.md](README.md) —— 架构、协议兼容、数据模型、路线图。
- [service/aiServeWeaveAgent/tunnel/README.md](service/aiServeWeaveAgent/tunnel/README.md) —— 隧道协议定义、连接状态机、槽池、实施阶段。改隧道相关代码前必读。

**README 与代码不一致视为缺陷。** 改公共 API 时同步更新对应文档，不要留到"以后补"。

## 代码地图

布局是 `service/<服务名>/`，完整结构见 README「代码结构」一节。各服务当前进度：

| 路径 | 状态 |
| --- | --- |
| `api/proto/` | 三边共享的 gRPC 契约，已生成 |
| `common/runtime/` | 推理后端抽象：能力探测、配额、流式转换。Agent 与 Gateway 共用 |
| `common/tunnelwire/` | `common/runtime` 类型与隧道 proto 之间的双向编解码，隧道两端共用 |
| `service/aiServeWeaveAgent/` | 主要实现所在：`tunnel/`（隧道）、`workflow/`（ComfyUI 工作流） |
| `service/aiServeWeaveGateway/` | `tunnelserver/`（隧道终结）、`scheduler/`（节点选择）、`httpapi/`（OpenAI 前门）均已落地；`e2e/`（与 Agent 的联调测试） |
| `service/aiServeWeaveRegistry/` | `NodeIdentity`（证书签发/续期）与 `GatewayDirectory`（副本名册）已落地，详见其 README |
| `service/aiServeWeaveConsole/`<br>`service/aiServeWeaveControlPlane/` | 尚无 Go 代码 |

动手前先确认目标服务是否已有实现，不要在骨架服务里凭空假设已有的包。

## 契约唯一源

`api/proto/tunnel/v1/tunnel.proto` 是 Agent、Gateway、Registry 之间唯一的契约来源，三边都 import 同一个生成包，不允许各自定义等价结构。

- 生成代码（`*.pb.go`）**不手工编辑**，改动只发生在 `.proto` 上。
- 重新生成：`go generate ./api/...`（需先装 `protoc-gen-go` 与 `protoc-gen-go-grpc`，见 [generate.go](api/proto/tunnel/v1/generate.go)）。
- 生成结果必须与仓库内文件一致，有 diff 说明没重新生成或工具版本不对。

## 质量门禁

改完代码全部跑通再提交：

```bash
gofmt -l ./service ./api      # 必须无输出
go vet ./...
go build ./...                # 三个服务入口都必须能链接
go generate ./api/...         # 结果须与仓库一致
go test ./...
go test -race ./service/...
```

## 编码约定

- 所有导出标识符有完整 doc comment，以标识符本身开头。
- 表驱动测试，每个用例有可读 `name`，失败信息同时包含期望值与实际值。
- 测试不用真实 `time.Sleep` 推进时间，一律通过注入的 `runtime.Clock` 控制。
- 协程泄漏在各包 `main_test.go` 的 `TestMain` 里统一断言，**不要**在单个测试里手写检查。
- 默认测试不依赖真实 Gateway、GPU 或外部网络；需要真实后端的测试单独隔离（参考 `runtime/ollama/live_test.go`）。
- 包内测试辅助放 `internal/`（如 `runtime/internal/runtimetest/`），不外泄给使用方。
- 新增第三方依赖要说明理由；标准库能解决的不引入依赖。

## 安全红线

以下几条被违反就是安全缺陷，不是风格问题：

- API Key、自定义鉴权头、完整 Prompt、工作流 JSON **不得**写入日志或错误文本，统一走 `runtime.Redact`。
- 跨隧道的 proto 转换只在 `common/tunnelwire` 里做，不在各服务里手写字段拷贝 —— 「凭据不过隧道」是靠那一个包保证的。
- Agent 只主动出站建连，从不监听公网端口。
- `runtime_id` 必须命中 Agent 本地白名单才执行 —— 即使 Gateway 被攻破也不能让 Agent 访问未声明地址。
- Agent 永远不做通用 HTTP 代理：隧道只传运行时语义，不传任意 URL、Host 或 Authorization。
- 任何一跳都不得无界缓冲：流式事件逐条转发，产物边读边送，背压靠 gRPC 流控向上游传导。

## 分层职责

隧道层不做能力判断、不做模型路由、不做重试 —— 这三件事分别属于 `runtime` 的能力门禁和 Gateway 的调度器。新增逻辑前先确认它该落在哪一层。
