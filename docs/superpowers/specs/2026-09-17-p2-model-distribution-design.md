# P2「模型分发」：现状核实、拆分与子任务一（Agent 本地校验和下载器）

本文档交付根 STATUS.md「P2：后续扩展」第四条：「模型分发：校验和、断点续传、磁盘配额与来源白名单。」这句话原样抄自 README.md:335（ComfyUI Managed 部署规划一节的一句前瞻性描述），此前从未有过任何设计文档或代码分析——不同于同一节其余大项（API 兼容边界、图像/Responses/多模态、资源感知调度）都已有各自的 `docs/superpowers/specs/*.md`，这一项是真正的从零开始。

范围边界：**本文档核实现状、划信任边界、拆出可独立排期的子任务列表；随后落地风险最低、最基础的子任务一**。其余子任务留给后续 session 排期，STATUS.md 对应条目本次不整体打勾，与「P2 API 兼容边界」条目（部分后续实现任务交付、条目整体仍标 `[ ]`）同一先例。

## 一、现状核实

### 1.1 README 里的描述是规划散文，不是规范

README.md:292 起的「托管部署（规划）」一节是 ComfyUI Managed 模式的前瞻描述；README.md:335 原句：「模型文件通常很大，Managed 模式支持挂载用户准备好的共享模型目录。模型分发是独立能力，需要校验和、断点续传、磁盘配额和来源白名单。」这句话把模型分发的必要性与 ComfyUI Managed 模式绑在一起，但没有给出协议、数据形状或触发机制，只是四个名词的罗列。

README.md:412-439「模型与部署抽象」一节定义了 `Node / Backend / Deployment / Model / Route` 概念表，`Deployment` 被描述为「某个 Backend 中运行的实际模型实例」——这是模型制品概念在架构图里唯一的落脚点。但 A04 设计文档已经核实过（STATUS.md:102 引用）：**全仓库没有 `Deployment` 类型**，这张概念表是尚未落地的图示，不是实现依据。本文档确认这一结论对模型分发同样成立：`common/modelroute.Target`（见 1.4 节）完全不引用这张概念表里的任何字段。

### 1.2 全仓库检索：零实现、零设计草稿

`grep -rn "模型分发"` 命中且仅命中 4 行，全部是描述性散文：

- STATUS.md:120（本条待办本身）
- README.md:335（上引）
- `common/runtime/README.md:1247`：Managed Runtime 路线图列项之一——「5. Managed Runtime：容器部署、模型分发、升级、排空和回滚。」与容器生命周期管理并列，未展开。
- `service/aiServeWeaveAgent/README.md:16`（「尚未交付的范围」一节）：「……模型分发和 Agent 自动升级仍属规划。本机自动发现不等于进程生命周期管理；已有隧道文件转发不等于对象存储或原节点离线后的文件可用性。」——这句明确警告不要把 P04 已交付的 `OPERATION_INPUT_UPLOAD` 隧道文件转发误当作模型分发的现成基础设施。

英文关键词（"model distribution"、"ModelPull"、"modeldistribution"）全仓库零命中。`docs/superpowers/specs/` 目录下没有匹配本主题的既有文档。

### 1.3 模型获取今天 100% 是运维手动责任

`common/runtime/ollama/runtime.go` 的包注释明确写着适配「一个已经在跑的 Ollama 服务器」，「Ollama 原生 generate 协议刻意不实现」；代码里只调用三个只读端点——`apiVersionPath = "/api/version"`、`apiTagsPath = "/api/tags"`（列出已有模型）、`apiShowPath = "/api/show"`（单模型能力元数据）。全仓库没有 `/api/pull` 调用，没有任何下载/拉取/fetch 逻辑。vLLM、SGLang 适配器的协议里同样没有拉取概念——它们期望模型已经在进程启动时从本地路径或 HF 缓存加载好，这条路径完全在本平台控制之外。

`service/aiServeWeaveAgent/localdiscovery/`（`localdiscovery.go`，177 行）只固定探测本机 `127.0.0.1:11434`（Ollama）和 `127.0.0.1:8000`（vLLM）两个回环端口*是否有服务在监听*（`Candidate{Kind, BaseURL}`），从不调用 `ListModels`，没有模型清单或存储路径的概念。

结论：从 Agent、Gateway 到控制面，没有任何代码路径会主动下载、拉取或以其他方式获取一个模型文件。

### 1.4 `common/modelroute.Target` 不携带制品信息

`common/modelroute/modelroute.go` 的 `Target` 结构（第 31-56 行）：

```go
type Target struct {
    RuntimeModel      string
    NodeSelector      map[string]string
    Priority          int
    Weight            int
    MinGPUMemoryBytes int64 // P2 资源感知调度新增
}
```

`RuntimeModel` 是后端已经认识的名字字符串（例如 `qwen3-coder:30b`），`NodeSelector` 按标签选节点——纯粹是「别名 → 后端地址」的映射，没有文件路径、校验和、字节大小等制品概念。该文件里唯一的 `sha256`/`Digest` 逻辑（第 132-140 行的 `Digest(routes []Route)`）是给**路由表 JSON 本身**算版本哈希，供 CAS 发布使用，与模型文件完整性无关，不要混淆。

### 1.5 Agent 依赖红线与可复用的实现先例

AGENTS.md：「Agent 与 Registry 的直接依赖只有 gRPC、protobuf 与 `coder/websocket`，这条线要守住。」`service/aiServeWeaveAgent/hostresources/hostresources.go` 的包注释明确引用这条红线作为它选择 `os/exec` shell out 到 `nvidia-smi`/`sysctl` 而非引入 `gopsutil`/NVML 绑定的理由——这是本任务应当遵循的既有实现idiom。

本任务用标准库 `net/http`（Gateway 侧 `controlplaneclient` 已经这样用，只是用在 Gateway 而非 Agent；Agent 现有 HTTP 客户端用法见 `common/runtime/ollama` 等适配器，同样是 stdlib）与 `crypto/sha256`（`common/modelroute/modelroute.go:6` 已经这样用）即可满足校验和、断点续传、配额、白名单四项要求，**不新增任何第三方依赖**，不违反红线。

### 1.6 `AllowedRuntimes` 白名单模式与它的默认方向为何不能照搬

`tunnel/dispatch.go`（第 56-60 行）与 `tunnel/client.go`（第 177-181 行）里的 `AllowedRuntimes []string`：空列表 = 放行全部。这个默认方向之所以安全，是因为它圈定的是「运维手动装在本机的东西」——`runtime_id` 白名单的威胁模型是「即使 Gateway 被攻破，也不能让 Agent 访问未声明地址」（AGENTS.md 安全红线），而「能装到本机」这件事本身已经隐含了运维批准，所以「未显式限制 = 全部批准」不违反这个威胁模型。

`docs/superpowers/specs/2026-09-15-a04-direct-mode-boundary-design.md`（第 44-46 行一带）已经论证过一个结构相同的问题：Direct 模式的地址白名单不能照搬 `AllowedRuntimes` 的默认方向，因为「网络可达」不等于「运维批准」，威胁模型不同，因此 Direct 地址白名单的默认方向必须反过来定为「空 = 拒绝」。

「模型来源白名单」是第三种独立的信任边界——「允许从哪里下载字节」——本文档在第三节显式重新推导它的默认方向，而不是机械复用 `AllowedRuntimes` 的「空=放行」。

## 二、范围与子任务拆分

把 STATUS.md 一句话拆成五个可独立排期的子任务：

1. **子任务一（本轮交付）**：Agent 本地、清单驱动的校验和下载器——一个纯 Agent 侧、standard-library 实现的工具包，运维通过本地 JSON 清单声明要拉取的模型制品，Agent 在启动时后台执行，覆盖校验和、断点续传、（单次运行）磁盘配额、来源白名单四项要求。不涉及隧道协议，不涉及 Gateway/控制面，触发权完全在节点本地。
2. **子任务二（未来）**：Gateway/控制面触发的拉取指令与状态回传。需要新隧道 `Operation`（可参考 `OPERATION_INPUT_UPLOAD` 立下的「header 后跟 `DataChunk`」分片框架的设计思路，但方向相反——只传控制指令和进度回报，不传模型字节，字节流仍由 Agent 主动对外部源发起）；还需要 `common/modelroute` 或新的 `Deployment`-like 契约，把「这个模型别名需要哪个制品」关联起来（当前完全缺失，见 1.4 节）。这个子任务会让子任务一的来源白名单从「防运维手误」升级为「防被攻破的 Gateway 诱导 Agent 拉取任意字节」的真实安全边界。
3. **子任务三（未来）**：Ollama 原生拉取。今天子任务一是一个通用的「按 URL+校验和下载单个文件」工具，不理解 Ollama 自己的 manifest+blob store 格式，因此不能让 Ollama 把下载好的文件当成一个模型来使用。真正给 Ollama 拉模型，应该 shell out 到 `ollama pull` 本身（复用 `hostresources` 的 `os/exec` 先例），而不是重新实现 Ollama 的存储格式。
4. **子任务四（未来）**：并发下载、跨重启的累计配额账本、OS 级可用磁盘空间探测（`hostresources` 增加磁盘字段，作为配额之外的二次防线）。
5. **子任务五（未来）**：Console 侧的拉取状态可视化——依赖子任务二先把状态回传到 Gateway/控制面。

M4 对 P2 的统一要求（STATUS.md:19）「各项先明确场景、依赖与验收，再拆分实施」在这里体现为：子任务一完全自洽、可独立验收、不依赖任何其余子任务；子任务二到五互相之间也没有循环依赖，子任务三/四/五各自依赖子任务一或二提供的基础能力，但彼此独立，可以按需插队排期。

## 三、「来源白名单」的信任边界推导

子任务一阶段，模型清单和白名单都来自节点本地 CLI flag——由运维直接配置在该 Agent 进程上，等同于「运维手动批准的东西」，看起来符合 `AllowedRuntimes` 的既有默认方向（空=放行）。

但本文档选择**现在就把默认方向定为「空 = 拒绝全部」**，理由：

- 子任务二一旦落地，这个白名单会从「本机配置防手误」变成「防被攻破的 Gateway 诱导 Agent 从任意地址拉取字节」的真实边界——这正是 A04 讨论过的、`AllowedRuntimes` 默认方向不适用的场景（网络可达的外部 URL，不是「运维装在本机」）。
- 如果子任务一先用「空=放行」的默认值上线，子任务二落地时就必须把默认值改成「空=拒绝」，这是一次会改变现有部署行为的破坏性变更（原本没配置白名单、靠空值放行的部署会突然全部被拒绝）。提前用安全默认值，避免这次行为突变。
- 这个功能本身默认关闭（`-model-pull-manifest` 不设置则完全不跑），先用更严格的默认值不会给任何现有部署增加操作负担。

允许列表用 URL 前缀字符串匹配，不做按解析 IP 的 SSRF 级校验——因为子任务一的 URL 来源是运维本机可信配置，不是「网络可达就默认可信」的场景，与 A04 讨论的 Direct 地址威胁模型不同。子任务二把触发权交给 Gateway 之后，这条判断需要重新评估（记入第五节已知缺口）。

## 四、子任务一详细设计

### 4.1 新包：`service/aiServeWeaveAgent/modelpull/`

纯 Agent 本地、标准库实现的「按清单校验和下载」工具：

```go
// Spec describes one model artifact to fetch and verify.
//
// Spec 描述一个要获取并校验的模型制品。
type Spec struct {
    Name       string // identifier used in logs and Result / 用于日志与 Result 的标识
    SourceURL  string // must match a Config.Allowlist prefix / 必须命中 Config.Allowlist 的某个前缀
    SHA256     string // required, hex-encoded / 必填，十六进制
    SizeBytes  int64  // known size; 0 = unknown / 已知大小；0 表示未知
    TargetPath string // atomically renamed here once verified / 校验通过后原子改名到这里
}

// Config controls how RunManifest fetches and verifies a Spec.
//
// Config 控制 RunManifest 如何获取并校验一个 Spec。
type Config struct {
    Allowlist  []string     // URL prefixes; empty rejects every pull / URL 前缀；空拒绝全部拉取
    QuotaBytes int64        // this run's byte budget; <=0 means unlimited / 本次运行的字节预算；<=0 表示不限
    HTTPClient *http.Client // nil uses http.DefaultClient / 为 nil 时用 http.DefaultClient
}

// LoadManifest reads a JSON-encoded []Spec from path.
//
// LoadManifest 从 path 读取 JSON 编码的 []Spec。
func LoadManifest(path string) ([]Spec, error)

// RunManifest processes each Spec in order: an already-present, checksum-matching
// target is skipped without touching the quota; otherwise it resumes a download
// into "<TargetPath>.part" via HTTP Range requests, verifies SHA256 on completion,
// and atomically renames on success. A failing Spec is recorded in Result.Failed
// and does not stop the remaining entries.
//
// RunManifest 按顺序处理每个 Spec：已存在且校验和匹配的目标直接跳过，不占用配额；
// 否则用 HTTP Range 请求续传到 "<TargetPath>.part"，完成后校验 SHA256，通过则原子
// 改名。失败的 Spec 记入 Result.Failed，不影响其余条目继续处理。
func RunManifest(ctx context.Context, cfg Config, specs []Spec) Result

// Result reports the outcome of a RunManifest call.
//
// Result 汇报一次 RunManifest 调用的结果。
type Result struct {
    Skipped []string
    Pulled  []string
    Failed  map[string]error
}
```

要点：

- **断点续传**：下载写入 `<TargetPath>.part`；若该文件已存在，用其当前大小发 `Range: bytes=<n>-`；服务端返回 206 才认为续传成立，否则（200 或其他）从零开始覆盖。
- **幂等/跳过**：`TargetPath` 已存在且其 SHA256 与 `Spec.SHA256` 一致时直接跳过，计入 `Result.Skipped`，不占用本次配额、不发起任何网络请求。
- **磁盘配额（本次运行）**：`QuotaBytes <= 0` 表示不限。`SizeBytes` 已知时在发起请求前做预检查——若 `SizeBytes` 减去 `.part` 已有大小仍超过剩余预算，直接跳过该条目并记入 `Result.Failed`，不发请求。`SizeBytes` 未知（0）时无法预检查，改为下载中流式计数：一旦本次运行累计新写入字节超过剩余预算，立即中止该条目（保留已写的 `.part`，供以后配额调高或换一次运行后继续），记入 `Result.Failed`。这是**单次运行的字节预算**，不做跨重启的累计账本（见第五节已知缺口）。
- **来源白名单**：空 `Allowlist` 拒绝全部条目（第三节推导的默认方向）；非空时要求 `SourceURL` 命中某个前缀，否则拒绝，不发请求。
- **不做自动重试/退避**：v1 每个 Spec 只尝试一次；失败即记录，依赖断点续传让"下一次调用"天然廉价。不引入 `runtime.Clock` 依赖——没有基于时间的重试或退避逻辑。
- **顺序执行**：`RunManifest` 不并发处理多个 Spec，避免配额账本在并发场景下的竞争问题（见第五节已知缺口）。

### 4.2 Agent 接入（`service/aiServeWeaveAgent/main.go`）

新增三个 flag，默认值使功能整体关闭、不影响现有部署：

```go
modelPullManifest  := flag.String("model-pull-manifest", "",
    "path to a JSON manifest of models to fetch and verify; empty disables model pulling")
modelPullAllowlist := flag.String("model-pull-allowlist", "",
    "comma-separated URL prefixes models may be pulled from; empty rejects every pull")
modelPullQuotaBytes := flag.Int64("model-pull-quota-bytes", 0,
    "byte budget for this run's model pulls; <=0 means unlimited")
```

仿照既有的 `startLocalDiscovery` 模式新增 `startModelPull(ctx, logger, cfg) <-chan struct{}`，在后台 goroutine 里运行，**不阻塞** Hello 握手/隧道连接建立——下载可能是大文件、耗时不可控，不应该让 Agent 看起来"卡死"或延迟接入 Gateway。完成或出错只记日志，best-effort、非致命，与 `hostresources`「探测失败留零值、不阻塞启动」的既有克制一致；区别在于 `hostresources` 是同步的（只是本机瞬时探测），这里必须异步（下载耗时无上限）。

### 4.3 测试

`modelpull_test.go`，表驱动，用 `httptest.Server` 覆盖：完整下载、Range 续传（模拟已存在的部分 `.part` 文件）、校验和不匹配（拒绝并清理 `.part`）、白名单拒绝（空列表/不匹配前缀两种）、配额预检拒绝（`SizeBytes` 已知）、配额流式中止（`SizeBytes` 未知）、已存在且校验和匹配的文件跳过、清单里部分条目失败不影响其余条目继续处理。全部不依赖真实网络。

### 4.4 文档同步

- `service/aiServeWeaveAgent/README.md`「尚未交付的范围」一节：把「模型分发……仍属规划」改为指向新增的「模型拉取（P2 子任务一）」小节，说明范围边界（本地清单驱动、无 Gateway 触发、不理解 Ollama 的 blob 存储格式）。
- STATUS.md 的「模型分发」条目：补充子任务一已交付的说明与已知缺口，条目整体保持未勾选状态（其余子任务未排期）。

## 五、已知缺口

如实记录，不阻塞子任务一验收，也不阻塞后续子任务排期：

- 不做 Ollama 原生 blob 存储格式集成（子任务三的范围）。
- 不做跨重启的累计配额账本——配额只在单次 `RunManifest` 调用内生效，Agent 重启后配额重新计满，可能允许超过运维预期的总磁盘占用（子任务四的范围）。
- 不做并发下载。
- 不做 OS 级可用磁盘空间（`statfs`）探测——`QuotaBytes` 是一个策略上限，不是对真实剩余磁盘空间的感知；配额设置过高仍可能把磁盘写满（子任务四的范围，且需要为 `hostresources` 补充磁盘字段）。
- 没有 Gateway/控制面/Console 可见性——拉取只在 Agent 本地日志里可见，运维必须直接查看节点日志或磁盘（子任务二、五的范围）。
- 来源白名单是前缀字符串匹配，不做按解析后 IP 的校验；子任务二把触发权交给 Gateway/控制面之后需要重新评估这一判断（第三节已说明理由）。
- `RunManifest` 每个 Spec 只尝试一次，没有自动重试/退避循环；运维需要自行重新触发 Agent 重启或后续加入的手动触发机制才能重试失败条目。
