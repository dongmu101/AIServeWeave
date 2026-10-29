# AIServeWeave 开发约定

分布式 AI 推理节点管理平台。文档、测试、注释与代码同等重要；Go 工具链以 `go.mod` 为准，Console 工具链以其 `package.json` 与局部约定为准。

## 开始工作

1. 先读 [README.md](README.md) 了解项目；涉及交付范围、待办或验收时，再读 [STATUS.md](STATUS.md) 与目标服务的 `STATUS.md`。区分代码已交付、默认测试通过和真实环境已验收。
2. 按下表读取当前改动需要的文档。已有实现以当前代码为准；发现文档不一致时，在本次涉及范围内同步修正。公共 API、配置与行为变更须同步对应文档。
3. 修改前确认目标包已有的实现、测试与调用边界；尚未实现的能力以 STATUS 为依据，不根据目录名或历史设计假定已经可用。

| 改动范围 | 必读入口 |
| --- | --- |
| 跨服务职责、协议、调度与数据模型 | [架构设计](docs/architecture.md) |
| Agent/Gateway 隧道与 proto | [隧道协议与状态机](service/aiServeWeaveAgent/tunnel/README.md) |
| Agent 配置、热重载与 CLI 账户额度 | [Agent README](service/aiServeWeaveAgent/README.md)、[Agent STATUS](service/aiServeWeaveAgent/STATUS.md)；CLI 适配再读对应后端包的 README |
| Gateway 兼容 API、路由、Job 与产物 | [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md)；协议支持范围和能力门禁以该文档与代码为准 |
| 控制面、Gateway 鉴权与凭据吊销 | [ControlPlane STATUS](service/aiServeWeaveControlPlane/STATUS.md) |
| Registry 身份、令牌与名册 | [Registry README](service/aiServeWeaveRegistry/README.md) |
| Console 页面、会话、转发与编辑器 | [Console AGENTS](service/aiServeWeaveConsole/AGENTS.md)；包含 Next.js 本地文档、TS 双版本、受限转发与验证要求 |
| 数据库 schema、迁移、升级与恢复 | ControlPlane STATUS 的「数据库」与 [数据库升级与恢复](deploy/database-recovery.md) |
| 部署、发布与真实环境验收 | [部署说明](deploy/README.md)、根 STATUS 中相关任务链接 |
| 第三方依赖 | [依赖边界与选型理由](docs/agents/dependencies.md) |

### CodeGraph

仓库根目录存在 `.codegraph/` 时，定位或理解代码先调用 `codegraph_explore`，查询中指定问题、符号或文件路径；也可执行 `codegraph explore "<问题或符号>"`。使用返回的当前源码和调用关系，避免重复搜索已覆盖的代码。配置、文档、未覆盖内容或明确标记过时的源码再直接读取。没有索引时使用 `rg` 等常规工具，不自行建立索引。

## 代码地图

布局与完整职责见 [架构设计「代码结构」](docs/architecture.md#代码结构)。本文件只保留选层所需的边界，功能完成情况由各服务 STATUS 维护。

| 路径 | 职责与边界 |
| --- | --- |
| `api/proto/tunnel/v1/` | Agent、Gateway、Registry 共用的 gRPC 契约与生成代码 |
| `common/runtime/` | 后端语义、能力门禁、适配器与并发控制；托管供应商适配器只由 Agent 注册 |
| `common/tunnelwire/` | runtime 类型与隧道 proto 的唯一双向编解码边界，包括结果标签约定 |
| `common/` 其他包 | 仅承载跨服务必须一致的契约或实现，例如 Key 哈希、配额、模型授权、路由与模板发布、允许列表视图、无凭据状态、指标与本机发现；复用已有包，避免各服务重定义 |
| `service/aiServeWeaveAgent/` | 本机配置、发现、运行时与 CLI、主动出站隧道；执行本地允许的操作 |
| `service/aiServeWeaveGateway/` | 兼容 API 前门、鉴权与配额、路由与调度、隧道终结、Job、产物存储与控制面同步 |
| `service/aiServeWeaveRegistry/` | 节点身份、证书与令牌、Gateway 副本名册 |
| `service/aiServeWeaveControlPlane/` | 管理与内部 API、会话、授权、数据库、发布、审计、历史指标与请求/用量记录、预付点数计费；Dodo Payments SDK 只在 `internal/billing/dodo` |
| `service/aiServeWeaveConsole/` | 管理界面与受限服务端转发；具体凭据和跨服务例外见其 AGENTS |
| `deploy/` | 部署、升级、恢复与验收操作说明 |

## 契约唯一源

- `api/proto/tunnel/v1/tunnel.proto` 是 Agent、Gateway、Registry 共用的 gRPC 契约，三边 import 同一个生成包，不各自定义等价结构。
- `*.pb.go` 不手工编辑。修改 `.proto` 后运行 `go generate ./api/...`；生成工具见 [generate.go](api/proto/tunnel/v1/generate.go)。生成结果必须与仓库中的契约一致。
- 跨服务的非 proto 契约复用对应 `common/` 包。控制面发布与 Gateway 执行必须使用同一套配额、模型授权、路由和模板语义。
- Console 手写的响应校验与契约镜像，在后端字段或语义变化时同步更新并验证，位置见 Console AGENTS。

## 分层职责

- 隧道层负责连接、传输、分发与背压；能力判断归 `runtime`，模型路由与节点选择、重试归 Gateway 路由层和调度器。
- 流式请求的重试与会话续接遵守已有边界；产生客户端可见输出后不得重放推理，CLI 工具续接须维持原会话的节点、身份与历史约束。
- 本地 CLI 的登录、账户目录与供应商凭据留在 Agent；CLI 订阅窗口、平台租户配额和单次推理用量是独立概念。
- 数据库变更沿用固定版本 SQL、校验和与 dirty 状态检查，不引入隐式 schema 漂移。各表的 PostgreSQL/MySQL 支持范围逐项确认，不能由基础表双支持推定所有表双支持。

## 安全红线

以下属于安全要求：

- API Key、自定义鉴权头、完整 Prompt、工作流 JSON 不得写入日志或错误文本；错误脱敏统一走 `runtime.Redact`。允许列表视图只输出契约明确声明的字段，不直接序列化内部 `NodeInfo`、`Descriptor` 或配置对象。
- runtime 与隧道 proto 的转换集中在 `common/tunnelwire`；凭据不进入隧道。推理后端的 Authorization 由 Agent 本地配置提供。
- Agent 主动出站建立隧道，不监听公网端口；本地设置页面只允许回环地址。
- `runtime_id` 必须通过 Agent 本地运行时允许列表检查；保留已有的空列表语义。模型下载来源、自定义节点、升级清单等分别遵守其本地授权与签名规则，不类推其他允许列表的空值含义。
- Agent 不提供通用 HTTP 或命令代理；隧道只携带运行时语义与封闭控制动作。下载来源、执行目标、镜像与路径按各功能的本地声明及受信清单校验，不由远端任意指定。
- 每一跳的缓冲、队列、请求体与并发都有界。流式事件逐条转发，产物边读边送，通过 gRPC 流控向上游传递背压。
- Console 的浏览器与服务端凭据边界、租户与平台会话隔离、转发允许列表、Job 直连 Gateway 和 ComfyUI 编辑器例外，严格沿用 Console AGENTS 的约束；新增端点不能沿用编辑器的前缀转发例外。

## 编码约定

- 新增或修改的导出标识符须有完整 doc comment，首行以标识符本身开头。
- 新增或修改的代码注释中英成对，英文在前、中文紧随其后，含义保持一致；行尾短注释可用 `// English / 中文`。生成代码与 `.proto` 注释不受此约束。仅在本次涉及的函数中补齐双语，保留未改动代码的原有注释。

  ```go
  // Consume validates token and marks it used. It returns ErrInvalidToken
  // when the token is unknown, already used, or expired.
  //
  // Consume 校验 token 并标记为已使用；token 未知、已使用或过期时返回 ErrInvalidToken。
  func (s *Store) Consume(token string) error {
      now := s.clock.Now() // one timestamp for the check / 整次校验共用一个时间戳
  ```

- 表驱动测试每个用例有可读 `name`，失败信息包含期望值与实际值。
- 测试通过注入的 `runtime.Clock` 推进时间，不用真实 `time.Sleep` 推进测试时钟。
- 协程泄漏在各包 `main_test.go` 的 `TestMain` 统一断言，不在单个测试里重复检查。
- 默认测试不依赖真实 Gateway、GPU、外部网络或真实数据库。真实后端与数据库测试单独门控，参考 `common/runtime/ollama/live_test.go` 和 ControlPlane live 测试。
- 包内测试辅助放在 `internal/`，例如 `common/runtime/internal/runtimetest/`，不外泄给使用方。
- 标准库能解决的问题不增加第三方依赖；新增依赖说明理由并检查服务边界，现有例外见 [依赖约定](docs/agents/dependencies.md)。

## 质量门禁

先运行改动相关的测试；Go 代码提交前完成以下门禁，与当前 CI 的 Go 检查保持一致：

```bash
gofmt -l ./service ./api           # 必须无输出
go vet ./...
go build ./...                    # 所有 Go 服务入口必须能链接
go generate ./api/...             # 核对 api/ 中生成差异与本次契约变更一致
go test ./...
go test -race ./service/...
```

涉及 `common/` 改动时，另运行 `gofmt -l ./common` 和所改共享包的 race 测试，补足当前 CI 对共享包格式与 race 的覆盖。

Console 改动执行其 AGENTS 中的 lint、build、typecheck、test；新增路由先 build 生成 `.next/types`。数据库、浏览器与真实后端验收按对应文档单独执行，不以默认测试通过替代真实环境验证。

纯文档改动检查路径、链接、命令与差异，无需运行 Go 或 Console 构建。交付时说明改动、验证结果及未能执行的检查，不把未执行的验证写成通过。

## Agent skills

### Issue tracker

任务以本地 Markdown 保存在 `.scratch/<feature>/`。创建、读取或更新任务前，阅读 [任务系统约定](docs/agents/issue-tracker.md)。公开贡献仍按 [CONTRIBUTING.md](CONTRIBUTING.md) 的 GitHub 流程进行。

### Triage labels

设置或解释任务状态前，阅读 [triage 标签映射](docs/agents/triage-labels.md)，沿用已确认的五种默认字符串。

### Domain docs

探索代码前，阅读 [领域文档规则](docs/agents/domain.md)。采用 single-context 布局，术语表与 ADR 按需创建。
