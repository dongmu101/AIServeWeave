# P2「模型分发」子任务三：Ollama 原生拉取

本文档交付 [P2 模型分发设计文档](2026-09-17-p2-model-distribution-design.md) 第二节拆出的子任务三：「Ollama 原生拉取。今天子任务一是一个通用的按 URL+校验和下载单个文件工具，不理解 Ollama 自己的 manifest+blob store 格式，因此不能让 Ollama 把下载好的文件当成一个模型来使用。」

## 一、现状与一处偏离原设计的重新权衡

子任务一的设计文档（第二节）当时设想给 Ollama 拉模型应该「shell out 到 `ollama pull` 本身（复用 `hostresources` 的 `os/exec` 先例）」。本文档核实后改用另一条路径：直接调用 Ollama 服务器自己的 `POST /api/pull` HTTP 接口，理由：

- **进度是结构化的**：`ollama pull` CLI 把进度渲染成终端进度条（`pulling 8934d96... 45% ▕████▏`），逐字节解析这种输出脆弱且容易被 Ollama 版本更新破坏；`POST /api/pull` 的 NDJSON 流每行是一个 `{"status","digest","total","completed"}` 对象，`common/runtime/ollama` 已经在用同一种 JSON 协议跟 Ollama 通信，风格一致。
- **不依赖 `ollama` CLI 二进制在 Agent 的 PATH 上**：Ollama 可能以容器方式运行（`common/runtime/ollama` 只要求「一个已经在跑的 Ollama 服务器」，从不假设本机装了 CLI）；HTTP API 只要网络可达即可，与该假设一致。
- **仍是标准库 `net/http`**：不新增依赖，不违反 AGENTS.md「Agent 与 Registry 的直接依赖只有 gRPC、protobuf 与 `coder/websocket`」这条红线；`hostresources` 的 `os/exec` 先例仍然成立，只是本任务判断这里不需要它——`os/exec` 是为了没有对应 HTTP 接口的探测（`nvidia-smi`、`sysctl`）而存在，Ollama 恰好有对应接口。

这是本文档与子任务一原设计文档唯一的分歧点，如实记录而非静默改写。

## 二、设计

### 2.1 `Spec.Kind`：一个 manifest 内的两种获取方式

复用子任务一已有的清单驱动模型（`-model-pull-manifest` 指向的 `[]modelpull.Spec`），不新开一份清单或一组 flag——运维已经在一份 JSON 文件里声明「这个 Agent 要有哪些模型」，Ollama 原生模型只是同一份清单里的另一种条目。`Spec` 新增 `Kind` 字段：

```go
type Kind string

const (
    KindHTTP   Kind = ""       // 子任务一：通用校验和下载器
    KindOllama Kind = "ollama" // 子任务三：Ollama 原生拉取
)
```

`Kind == KindOllama` 的条目：

- `Name` 就是 Ollama 期望的模型 tag（例如 `qwen3-coder:30b`），直接传给 `POST /api/pull` 的 `model` 字段——不新增一个「显示名」与「Ollama tag」分离的字段，一份 manifest 条目的名字有且只有一个含义，KISS。
- `SourceURL`、`SHA256`、`TargetPath` 必须留空，`validateSpec` 拒绝设置了它们的 `kind: "ollama"` 条目——这三个字段的含义都建立在「Agent 自己管理这个文件」之上，而 Ollama 原生拉取里文件完全在 Ollama 自己的 blob store 里，Agent 不接触。

### 2.2 `Config.OllamaBaseURL`：复用 `-ollama-url`，不新增 flag

`modelpull.Config` 新增 `OllamaBaseURL string`。Agent 侧 `main.go` 的 `newModelPuller` 直接传入已有的 `-ollama-url`（该 flag 本来就是「这个 Agent 要注册的那一个 Ollama 实例」），不新增 `-model-pull-ollama-url` 一类的重复配置——运维心智模型里「Agent 认识哪个 Ollama」只有一个答案，不应该在两处分别配置、可能配出不一致。

`OllamaBaseURL` 为空（即 `-ollama-url` 未配置）时，任何 `KindOllama` 条目在发起请求前就被拒绝为 `ReasonOllamaUnconfigured`——manifest 可以在没有 Ollama 运行时的 Agent 上照样声明 `KindOllama` 条目（不报错、不阻塞启动），只是永远不会成功，直到运维配置了 `-ollama-url` 为止；这与 `-ollama-url` 本身「留空则不注册运行时」的既有语义对称。

### 2.3 `pullOllama`：调用 `POST /api/pull`，NDJSON 流式解析

新文件 `service/aiServeWeaveAgent/modelpull/ollamapull.go`：

```go
func pullOllama(ctx context.Context, client *http.Client, baseURL string, spec Spec, onProgress func(downloaded, total int64)) error
```

请求体 `{"model": spec.Name, "stream": true}`；响应用 `json.Decoder` 逐行 `Decode`，每行映射到：

```go
type ollamaPullChunk struct {
    Status    string
    Total     int64
    Completed int64
    Error     string
}
```

- 行里出现非空 `Error` 字段：Ollama 服务器自己报告了拉取失败（例如未知的模型 tag），归类为新的哨兵错误 `errOllamaPullFailed`，与「请求根本没到达服务器」的 `errFetchFailed` 区分开。
- `status == "success"`：成功返回。
- 流在看到 `success` 之前就 EOF（连接被服务端提前关闭）：归类为 `errFetchFailed`——「流断了」与「服务器说了具体原因」是两种不同的失败，值得在日志里分得清楚，即使两者上报到 Gateway 的封闭 Reason 目前都可能是 `fetch_failed`/`ollama_pull_failed` 之一，不影响调用方，但保留 Agent 本地日志的可诊断性。
- `Total`/`Completed` 描述的是**当前正在获取的那一个 layer（blob）**，不是整个模型的累计进度——Ollama 的这个 API 不提供跨 layer 的总和。`onProgress` 因此只是「大致在动」的信号，不是精确的完成百分比；这是协议本身的限制，不是实现取舍。

### 2.4 失败原因：两个新的封闭枚举值

`common/modelpullstatus.FailureReason` 新增：

```go
ReasonOllamaUnconfigured // Config.OllamaBaseURL 为空
ReasonOllamaPullFailed   // Ollama 服务器自己报告的错误
```

`tunnel.proto` 的 `ModelPullFailureReason` 枚举对应追加 `MODEL_PULL_FAILURE_REASON_OLLAMA_UNCONFIGURED = 9`、`MODEL_PULL_FAILURE_REASON_OLLAMA_PULL_FAILED = 10`（只追加，不改动既有值，保持线上兼容），`common/tunnelwire` 补上两个方向的映射。`Spec.Kind` 本身**不**过隧道——`ModelPullTrigger`/`ModelPullReport` 都只携带名字与状态，Gateway 从不需要知道一个名字背后是 HTTP 下载还是 Ollama 拉取，这与子任务二「只按名字触发」的既有边界完全一致，不需要为 `Kind` 单独设计线上表示。

### 2.5 `Puller` 的分派

`Puller.runOne` 在 `validateSpec` 通过后按 `spec.Kind` 分派：`KindOllama` 走新增的 `runOllama`（调用 `pullOllama`），其余走原有的 `alreadySatisfied`/`sourceAllowed`/`pullOne` 路径。`RunManifest`（子任务一的同步入口，Agent 启动时仍会调用一次）同样按 `Kind` 分派。两处都不引入一个跨 Kind 的通用「dispatch」抽象——两条路径需要检查的前置条件完全不同（HTTP 需要白名单与已存在校验，Ollama 不需要），强行统一只会让分支逻辑更难读。

## 三、已知缺口

如实记录，不阻塞本子任务验收，留给运维认知或后续子任务：

- **配额不适用于 `KindOllama`**：`Config.QuotaBytes` 只约束 `KindHTTP` 条目。真正接收并写入字节的是 Ollama 服务器自己的进程，不是 Agent；取消 Agent 发起的这次 HTTP 请求，并不能让 Ollama 服务器停止继续下载并写入它自己的 blob store（Ollama 内部会话与 Agent 的这次 client 请求是解耦的）。给 `KindOllama` 挂一个「取消了但没真正停止」的配额，比不做更容易误导运维,因此选择明确不支持，而不是提供一个看似生效、实际不可靠的开关。
- **来源不受 `-model-pull-allowlist` 约束**：`KindOllama` 条目没有 `SourceURL`，Ollama 从它自己配置的注册表（通常是 `https://registry.ollama.ai`，或其服务端 `OLLAMA_HOST`/镜像配置指向的别处）拉取，这完全在 Agent 控制之外。信任边界仍然成立：能触发的名字必须先出现在 Agent 本地 manifest 里（`ReasonUnknownName` 的既有拒绝逻辑对 `KindOllama` 同样生效），子任务二设计文档给出的「防被攻破的 Gateway 诱导 Agent 拉取任意字节」威胁模型没有削弱——攻击者最多能让 Agent 提前触发一个运维本就声明过的模型名。
- **进度是近似值**：如 2.3 节所述，`BytesDownloaded`/`BytesTotal` 反映当前 layer 而非整个模型，`Done` 状态里这两个字段也只是最后一次观测到的 layer 数值，不是模型总大小。
- **没有跨 Gateway/控制面/Console 的额外改动**：`Spec.Kind` 完全是 Agent 本地概念，子任务二已交付的触发/查询/控制面转发/Console 可见性（分别见其各自的设计文档与 STATUS.md P2「ComfyUI Managed」子任务五）对 `KindOllama` 条目**天然可用**，不需要为此新增任何 Gateway/控制面代码——这是「只按名字触发」这条既有设计选择的直接红利，不是本子任务额外做的工作。
- **不做「模型已存在」的本地判断**：不像 `KindHTTP` 的 `alreadySatisfied`（比对 `TargetPath` 的 SHA256），`KindOllama` 每次触发都直接调用 `POST /api/pull`，依赖 Ollama 自己的幂等实现（已有模型很快答复 `success`）。这更简单也更正确——Agent 没有可靠途径在不问 Ollama 的情况下知道它是否已经有某个 tag（`common/runtime/ollama` 的 `ListModels` 可以查，但引入这一步只是把 Ollama 已经做的判断在 Agent 侧重做一遍，没有必要）。
