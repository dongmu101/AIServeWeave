# P2「模型分发」子任务四：并发下载、跨重启配额账本与磁盘二次防线

本文档交付 [P2 模型分发设计文档](2026-09-17-p2-model-distribution-design.md) 第二节拆出的子任务四：「并发下载、跨重启的累计配额账本、OS 级可用磁盘空间探测（`hostresources` 增加磁盘字段，作为配额之外的二次防线）。」

## 一、现状

子任务一/二/三交付后，`modelpull` 有三处已在设计文档里如实记录为子任务四范围的缺口（`docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md` 第五节）：

- `RunManifest`/`Puller` 都是顺序处理——同一时间只有一个下载，多个大模型排队会互相等待。
- `Config.QuotaBytes` 是**单次调用**（`RunManifest` 一次调用，或 `Puller` 一次 worker session）的预算，Agent 重启后配额重新计满，运维设的"总共不超过 N GB"约束无法真正生效。
- 没有对真实剩余磁盘空间的感知——`QuotaBytes` 只是策略上限，设置过高仍可能把磁盘写满；`hostresources` 目前只探测 CPU/内存/GPU。

## 二、设计

### 2.1 并发下载：`Config.MaxConcurrency`，默认值保持顺序

`Config` 新增 `MaxConcurrency int`。`<= 1`（含零值）保持现状——逐一处理，这是子任务一/二/三测试已经依赖的行为，不能默认改变。`> 1` 时：

- `RunManifest` 用一个大小为 `MaxConcurrency` 的信号量并发处理 `specs`，用 `sync.WaitGroup` 收敛；`Result` 的三个字段（`Skipped`/`Pulled`/`Failed`）在一把 `sync.Mutex` 后面聚合——顺序不再保证与 `specs` 的顺序一致，这是并发本身的代价，`Result` 本就是无序集合（`Failed` 是 map），不引入新的契约。
- `Puller` 不再是"队列 + 单个 worker goroutine"，改为"队列 + 最多 `MaxConcurrency` 个 worker goroutine"：`worker()` 不变（仍是"取一个名字、跑、取下一个"的循环），`Trigger` 在队列从空变非空时按 `min(MaxConcurrency, 1)` 启动相应数量的 worker；`p.running` 布尔改为 `p.activeWorkers int` 计数，最后一个退出的 worker 才把队列判定为"可以再次从零启动"。
- **每个 Spec 内部仍不并发**——一次下载仍是一条 HTTP 请求、一个 `io.Copy`，并发只发生在"多个不同的 Spec 之间"，不改变单个大文件下载的行为，也不需要 HTTP Range 分片这类更复杂的机制。

并发直接暴露了 `quotaWriter` 原有实现的一个前提：`*budget -= n` 在多个 goroutine 共享同一个 `*int64` 时不是原子操作。改为 `budget *atomic.Int64`（`Config`/`Puller`/`pullOne` 沿用同一个类型，取代原来的 `*int64`），`quotaWriter.Write` 用 `Add`/`CompareAndSwap` 风格的循环代替直接减法，防止两个并发下载在预算边界附近都通过检查、合计超支。

`KindOllama` 条目不受 `MaxConcurrency` 限制——`pullOllama` 本来就没有配额，且 Ollama 服务器自己的拉取会话与 Agent 发起的这次 HTTP 请求解耦，Agent 侧的并发数对它没有实际意义；但仍然按 `MaxConcurrency` 占用一个并发槽位，因为 Agent 侧这次请求本身占着一个 goroutine 和一个 `http.Client` 连接。

### 2.2 跨重启配额账本：`Config.Ledger`

新文件 `service/aiServeWeaveAgent/modelpull/ledger.go`，新类型 `Ledger`：

```go
type Ledger struct {
    Path   string        // JSON 文件路径，运维配置
    Period time.Duration // 账本的滚动窗口；<= 0 表示账本永不重置（真正的"生涯总量"）
    Clock  runtime.Clock
}
```

账本文件内容是一个最小的 JSON 结构：

```go
type ledgerState struct {
    PeriodStart time.Time `json:"period_start"`
    BytesUsed   int64     `json:"bytes_used"`
}
```

`Ledger.Reserve(n int64) (rollback func(), err error)`：

- 读取现有文件（不存在视为全新账本，`PeriodStart` 设为 `Clock.Now()`）；`Period > 0` 且 `Clock.Now()` 已经超过 `PeriodStart + Period` 时，账本先重置（`BytesUsed = 0`，`PeriodStart = Clock.Now()`）——与 `Config.QuotaBytes` 单次调用配额的"重启即清零"完全不同语义，这里是"到点才清零"，这正是它作为跨重启账本存在的意义。
- 校验 `cfg.Ledger.LedgerQuotaBytes`（见下）：`BytesUsed + n` 超出时返回 `errQuotaExceeded`（复用既有哨兵错误，不新增一个），不写入文件。
- 通过则先把 `BytesUsed += n` 写回文件（原子改名，同 `pullOne` 落盘模式：写临时文件再 `os.Rename`），再返回。
- 返回的 `rollback` 在调用方最终发现实际写入字节数小于预留的 `n`（例如下载中途失败，只写了一部分）时调用，把差额还回账本——账本记的是"实际落盘的字节"，不是"曾经打算下载的字节"，否则反复失败的条目会不公平地过早耗尽账本。

`Config` 新增：

```go
Ledger           *Ledger // nil 表示不启用跨重启账本，行为与子任务一/二/三完全一致
LedgerQuotaBytes int64   // <= 0 表示账本本身不限（仍可能被 QuotaBytes 单次预算限制）
```

`Config.Ledger` 与 `Config.QuotaBytes` 是两把独立生效的闸门，不是二选一：`QuotaBytes` 挡的是"这一次运行/session 别一口气下太多"，`Ledger` 挡的是"这台机器这个周期内累计别下太多"，两者的失败原因也分开——账本超限记为新增的 `ReasonLedgerQuotaExceeded`，不复用 `ReasonQuotaExceeded`，因为运维看到日志需要知道该调哪个数字。

`pullOne` 在写入每个 chunk 前先向 `Ledger`（若配置）预留字节数，账本拒绝时中止下载（保留 `.part`），这与 `Config.QuotaBytes` 现有的"流式中止、允许以后续传"完全同构，唯一区别是预算来源不同、账本会跨进程持久化。为避免每个字节都读写一次文件，预留按 chunk（`io.Copy` 的默认 32KiB 缓冲区）粒度进行，不是按字节。

**已知取舍**：账本文件不加进程间文件锁——Agent 本来就不支持同一份 `TargetPath`/清单被两个 Agent 进程同时处理（这从未是设计假设的一部分），单进程内的并发写由 `Ledger` 自己的 `sync.Mutex` 序列化即可，不需要 `flock`。

### 2.3 磁盘二次防线：`hostresources.DiskFreeBytes`

新增 `hostresources.DiskFreeBytes(path string) (int64, error)`：Linux/Darwin 用标准库 `syscall.Statfs`（两个平台的 `Statfs_t` 都有 `Bavail`/`Bsize` 字段，含义一致，无需 shell out，因此不落入本包其余探测项"必须 os/exec 系统自带工具"的模式——这里标准库本身就够）；其他平台返回错误，与本包"探测不到就返回错误、调用方自行决定要不要当回事"的既有约定一致。`hostresources.Detect` 不调用它——`NodeResources` 是节点静态容量的握手快照，磁盘剩余空间是易变的运行时状态，混进握手字段会造成"labels 描述机器、心跳描述负载"这条既有边界外的第三种语义，因此如实收窄为一个独立的、供 `modelpull` 直接调用的函数，不过隧道、不进 proto。

`Config` 新增 `DiskFreeMarginBytes int64`（`<= 0` 关闭这项检查）。`pullOne` 在预留 `Ledger`/`QuotaBytes` 之后、写入每个 chunk 之前，额外查一次 `spec.TargetPath` 所在文件系统的剩余空间，低于 `DiskFreeMarginBytes` 时中止（保留 `.part`），分类为新增的 `ReasonDiskSpaceLow`。这是**尽力而为的二次防线**，不是精确的资源预留——两次 `DiskFreeBytes` 调用之间，同一台机器上的其他进程仍可能抢占磁盘，与 `hostresources` 包文档一贯的"这是容量信息，不是强一致的保证"立场一致；同理，`KindOllama` 条目不做这项检查——它的字节写入根本不经过 `pullOne`，磁盘由 Ollama 自己的进程管理。

### 2.4 失败原因：两个新的封闭枚举值

`common/modelpullstatus.FailureReason` 新增 `ReasonLedgerQuotaExceeded`、`ReasonDiskSpaceLow`；`tunnel.proto` 的 `ModelPullFailureReason` 对应追加 `= 11`、`= 12`（只追加），`common/tunnelwire` 补上两个方向的映射，做法与子任务三新增的两个值完全相同。

## 三、已知缺口

- **账本粒度是"整个 Agent"，不区分 `KindHTTP` 具体名字**：一个模型反复失败重试会比一次性下载消耗更多账本额度（rollback 只归还"少写的"，不归还"曾经写过又因校验和不匹配被删除的"），这是刻意的——账本约束的是磁盘写入总量这个物理事实，不是"这次尝试是否最终成功"。
- **`DiskFreeBytes` 在 Windows 或其他非 Linux/Darwin 平台永远失败**：与 `hostresources` 现有 GPU 探测"仅 Linux+NVIDIA"同一先例，`DiskFreeMarginBytes` 配置在这些平台上等价于关闭。
- **并发数与磁盘 I/O 带宽的关系不做任何调度**：`MaxConcurrency` 由运维直接设定一个数字，不根据探测到的磁盘类型（HDD/SSD/网络存储）自动调整；这本就是运维今天设置 `-model-pull-quota-bytes` 之类参数时已经承担的责任，不是本子任务新增的负担。
