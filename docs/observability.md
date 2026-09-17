# 可观测性

指标经 `runtime.Metrics` 抽象记录，`common/metrics` 提供注册表与 Prometheus 导出。指标定义与记录点放在同一模块，服务装配时统一注册。追踪通过请求关联标识连接 Gateway、Scheduler、Tunnel、Agent 和后端；后端不支持传播时明确链路边界。

观测范围包括节点连接与心跳、部署健康、请求量与并发、TTFT、总时长、token 用量、吞吐、错误与重试、ComfyUI 队列及任务时长、GPU OOM、产物传输与存储用量。

指标端点应处于受控网络；节点标识涉及资产信息。标签来源必须受控且基数有界，模型名、请求路径、request ID、Prompt、工作流 JSON 和任意错误文本不能直接成为标签。具体指标、端点配置与实现边界见 [Gateway README](../service/aiServeWeaveGateway/README.md) 和 [隧道 README](../service/aiServeWeaveAgent/tunnel/README.md)。

历史曲线、告警、请求检索和用量账本具有不同的数据保留与授权需求。Console 通过控制面授权查询，不能把副本的实时指标当作历史统计。可用性、延迟、恢复时间与可接受数据丢失范围应有量化验收口径，数值在容量与故障测试后确定。
