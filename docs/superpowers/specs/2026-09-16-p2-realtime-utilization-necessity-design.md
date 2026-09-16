# P2-1 子任务 3 必要性评估：实时利用率采集是否值得做

[`2026-09-15-p2-resource-aware-scheduling-design.md`](2026-09-15-p2-resource-aware-scheduling-design.md) 第八节把「实时利用率采集与协议扩展」列为子任务 3，并明确设了一道门槛：「需要先证明静态过滤（任务 2）不足以满足场景后再排期，避免过度设计」。任务 2（`pickBy` 的 `nodeHasCapacity` 容量过滤）已经交付（STATUS.md 第 121 行），本文档就是那道门槛要求的证明，只回答"值不值得做"，不产出实现代码。

## 一、结论

**值得做，但范围比原设计文档设想的更窄。** 证据集中在 ComfyUI 一种运行时上；本文档找不到证据说明 Ollama/vLLM/SGLang 也存在同等缺口，因此不建议把子任务 3 做成通用的"CPU/内存/显存利用率"协议扩展，只建议扩展到 ComfyUI 已有但未接入调度的队列深度信号。

## 二、静态过滤为什么不够：一个已经在代码里成立的反例

`nodeHasCapacity`（`service/aiServeWeaveGateway/scheduler/scheduler.go:544`）只比较节点声明的 GPU 显存**总量**与 target 要求的最小值，不涉及当前占用：

```go
func nodeHasCapacity(node tunnelserver.NodeInfo, minBytes int64) bool {
	if minBytes <= 0 || node.Resources == nil || node.Resources.GpuMemoryBytes <= 0 {
		return true
	}
	return node.Resources.GpuMemoryBytes >= minBytes
}
```

`pickBy` 排序用的另外两个信号——`idle`（`node.IdleSlots[SLOT_CLASS_INFERENCE]`）与 `inflight`（`node.InflightRequests`，scheduler.go:499-524）——来自 `common/runtime.Config.MaxConcurrent` 的软限流器，默认值 32（`common/runtime/config.go:18`）。Agent 对全部 runtime kind（含 ComfyUI）一视同仁地套用这个默认值：`service/aiServeWeaveAgent/main.go:430` 只注册了 `runtime.KindComfyUI: comfyui.New`，没有任何 ComfyUI 专属的并发上限覆盖；`service/aiServeWeaveAgent/README.md` 与 `deploy/` 下的文档里也没有一处告诉运维要把 ComfyUI 的并发上限调成 1。

但 A06 的注释（`common/runtime/workflow/comfyui/metrics.go:20-27`）已经写明 ComfyUI 的物理限制："ComfyUI 在非定制构建下同一时间至多运行一个"。也就是说：**除非运维手动配置，一个正在用满显存渲染一个任务的 ComfyUI 节点，`idle` 读数最多仍能显示到 31（32 减去 1 个在途）**——调度器会认为它几乎完全空闲，把下一个大显存工作流继续派给它，而不是转去一个真正空闲的节点。这正是设计文档 1.3 节点名的场景（"队列很浅但每个任务都吃满显存"），但不再是假设：默认配置下这个场景必然发生，不需要任何异常条件触发。

容量过滤（任务 2）对此无能为力，因为它比较的是"总量"，而这里缺的是"当前剩余量"——两个 target 要求相同、声明的 `GpuMemoryBytes` 也相同的候选节点，一个空闲、一个正在渲染，`nodeHasCapacity` 对两者给出完全相同的判断。

## 三、已有信号：A06 的队列深度没有接入调度

`common/runtime/workflow/comfyui/metrics.go:28/32` 定义的 `comfyui_queue_running`/`comfyui_queue_pending` 正是能区分"占用中"与"空闲"的信号，取自 `GET /queue` 的数组长度（`runtime.go:407-411` 在 `Status` 里、`runtime.go:556-560` 在 `Cancel` 里各调用一次）。但这两处调用点都是**按具体某个 job 的状态查询触发**，不是按节点的稳定周期——没有任何客户端在轮询某个 job 时，这个信号就不会刷新。真正稳定按周期跑的是 `Health`（`runtime.go:182-205`，`defaultHealthInterval` 默认 10 秒，`common/runtime/config.go:16`），它特意只调用 `GET /system_stats`，不碰 `GET /queue`——注释写明原因是"a server busy rendering still reports promptly"，即保持健康检查本身足够轻，不与渲染任务抢队列端点。

结论：A06 已经生产出正确的信号，但这个信号今天：(a) 只在有 job 被轮询时才新鲜，(b) 只到 Prometheus 导出为止，从未回流到 `tunnelserver.NodeInfo`/`pickBy` 能读到的地方。这不是"信号不存在"（子任务 1/2 的处境），而是"信号存在但没接线"，两种缺口的补救成本不同。

## 四、协议改动范围比原文档设想的小

原设计文档第四节担心子任务 3 需要"新的 Heartbeat 字段或独立的资源上报通道"。但 `RuntimeSnapshot`（`api/proto/tunnel/v1/tunnel.proto:582-590`）已经把 `HealthReport health = 4` 按 runtime 逐个带到 Gateway，与 `InflightRequests`、`Discovery` 走同一条已经打通的通道，不是轻量级的 `Heartbeat` 消息（`SentUnixMs`/`InflightRequests`/`IdleSlots` 三字段）。也就是说，真正需要的协议改动是给 `HealthReport`（tunnel.proto:610-615）新增一个可选字段（例如 `int32 queue_pending`），而不是发明新通道或扩展 `Heartbeat`。这仍然是一次「契约唯一源」流程内的 proto 改动（AGENTS.md 要求 `go generate ./api/...` 且结果与仓库一致），但范围收窄到一个消息、一个字段。

代价是 `Health(ctx)` 必须多发一次 `GET /queue` 才能让这个字段保持新鲜（否则复用 Status/Cancel 顺带写入的值可能长期陈旧）——这与它目前刻意只查 `/system_stats` 的设计冲突，需要在实现阶段权衡"健康检查变重一点"与"排序信号新鲜度"，属于子任务 3 实现时才需要解决的问题，本文档不代为决定。

## 五、为什么不建议扩展到 Ollama/vLLM/SGLang

这三个后端共用 `common/runtime/internal/oaibase`（`oaibase.go:100` 同样用 `runtime.NewLimiter(cfg.MaxConcurrent)`），但它们是真正支持批处理并发的服务器，`MaxConcurrent` 描述的是"我打算允许多少并发请求"这个运维决策，不是像 ComfyUI 那样在掩盖一个物理上只能串行的后端。本文档没有找到证据表明这三者的 idle/inflight 信号与真实资源压力脱节到需要新信号的地步——这条判断沿用 A05 第四节"先用已有信号做低成本实现、再评估精确方案"的方法论，没有证据支持时不预先设计。

## 六、建议范围（供后续实现任务排期用，本文档不实现）

1. `HealthReport`/`RuntimeSnapshot` 新增一个可选字段承载 ComfyUI 的队列占用信号（具体是 `queue_running`、`queue_pending` 还是两者合一的布尔"busy"，留给实现阶段决定），仅 ComfyUI 适配器填充，其余 runtime kind 保持零值。
2. `Health(ctx)` 是否需要多查一次 `/queue`、还是复用最近一次 Status/Cancel 观测值，需要实现阶段权衡陈旧度与后端额外流量，本文档不替代该决策。
3. `pickBy` 消费方式沿用原设计文档第五节第 3 条的两步法：先只用于排序（占用高的候选排后面），不做过滤——过滤应保留给"容量不足"这类确定性判断，占用是易变的瞬时值，不适合直接排除候选。
4. 范围明确排除 Ollama/vLLM/SGLang 的通用 CPU/内存/显存利用率协议扩展；如果未来有真实证据（例如生产环境观测到这些后端也出现类似的信号与实际压力脱节），应另开一份必要性评估，不要直接复用本文档的结论。

## 七、与 STATUS.md 的关系

本文档满足子任务 3 设计门槛"先证明任务 2 不足"的要求：第二节给出一个默认配置下必然成立、而非假设性的反例，第三节说明 A06 已经产出所需信号但未接入调度，第四节纠正了协议改动范围的估计，第五节说明为什么不把范围扩大到其他运行时。结论是子任务 3 可以排期，范围收窄为"ComfyUI 队列占用信号接入 `HealthReport`/`pickBy`"，不是通用资源利用率协议。STATUS.md 对应行据此更新，标注为"必要性评估已完成，可排期，范围见本文档"，不等同于该子任务已实现。
