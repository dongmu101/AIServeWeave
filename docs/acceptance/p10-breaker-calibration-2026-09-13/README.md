# P10 真实流量熔断校准 · 2026-09-13

本记录回应 [P10 验收手册](../../../deploy/p10-acceptance.md)「真实流量熔断校准」一节列出的唯一剩余缺口：`scheduler` 包的熔断默认值（连续失败 5 次、初始冷却 5s、最大冷却 2m）此前从未用真实推理后端与真实隧道验证过。本轮：

1. 在 `service/aiServeWeaveGateway/main.go` 新增 `-breaker-failure-threshold`/`-breaker-base-cooldown`/`-breaker-max-cooldown` 三个 CLI flag（默认 `0`，即沿用 `scheduler` 包内置默认值，零值不改变现有行为），使候选参数可以在不重新编译的前提下对比。
2. 用这三个新 flag 起了两套候选参数，跑了真实单机环境：ControlPlane + Registry（Docker Compose）+ 一个 host 进程 Gateway + 一个 host 进程 Agent + 本机真实 Ollama，做了四类场景（稳定负载、中度饱和、硬饱和、真实断连与恢复）。

**范围声明（务必先读）**：这是单机、单 Gateway 副本、单 Agent、单 Ollama 实例的第一手真实流量数据点，不代表多节点/多 GPU 生产规模，也不是容量或 SLO 承诺。默认值**没有**被修改；`service/aiServeWeaveGateway/scheduler/breaker.go` 的三个 `default*` 常量保持原样。本记录只给维护者一份决策输入。

## 环境

| 项 | 值 |
| --- | --- |
| 机器 | Apple M1 Max，10 核，64GB 内存，macOS 26.6.2（Darwin 25.6.0） |
| Go | go1.27.1 darwin/arm64 |
| Ollama | 0.34.0（本机 Ollama.app，非容器） |
| 模型 | `gemma4:26b`（architecture gemma4，25.2B 参数，Q4_K_M 量化，含 `thinking` 能力） |
| 拓扑 | 1 Registry + 1 ControlPlane（`deploy/docker-compose.yaml` 的 `postgres`/`redis`/`registry-init`/`registry`/`controlplane`，未启动 `console`/`bootstrap`/compose 自带的 `gateway`）+ 1 Gateway host 进程 + 1 Agent host 进程 + 1 真实 Ollama 实例 |
| 节点 | `local-mac-agent-1`，`labels=region=local,gpu=apple-silicon`，mTLS 节点证书由 Registry 真实签发 |
| 凭据 | 真实 ControlPlane 租户/owner/API Key（tenant `BreakerCalib`），走 `deploy/.env` 里已有的 `AISW_BOOTSTRAP_TOKEN`/`AISW_INTERNAL_TOKEN`，未改动 `.env` |
| SlotHint | `MinSlots=2 MaxSlots=8 BulkSlots=1`（`main.go` 硬编码值，未改动） |

**模型选型**：候选池是 `ollama list` 里已拉取的 6 个 17–21GB 模型（无小模型可用）。warm 单请求延迟实测：`qwen3.8:27b-mlx` 约 3.1s，`gemma4:26b` 约 1.3s——选后者以加快迭代。

**Registry 管理面**：`deploy/docker-compose.yaml` 默认未给 `registry` 传 `-admin-token-file`，`-mint-token` 因此不可用。本轮临时给 `registry` 命令加了一行 `-admin-token-file=/data/admin-token.txt`（配一份现场生成的 45 字节密钥，仅活在 registry 的具名卷里）以便签发 bootstrap token；这处 compose 改动只为本次实验存在，**不建议合并**，工作区里保留该行供复核，验收结束后应还原。

## 真实推理链路验证

起环境后先用一次非流式请求确认「Console 未涉及、ControlPlane 签发的 key → Gateway 校验 → 隧道 → 真实 Agent → 真实 Ollama」整条链路：`curl /v1/chat/completions` 返回 `200`，`finish_reason":"length"`（`gemma4:26b` 开着 `thinking`，`max_tokens=48` 时常被思考 token 占满，属实测现象不是故障）。

## 两个候选参数

| 候选 | `FailureThreshold` | `BaseCooldown` | `MaxCooldown` | 复现命令片段 |
| --- | --- | --- | --- | --- |
| A（当前默认） | 5 | 5s | 2m | `-breaker-failure-threshold 5 -breaker-base-cooldown 5s -breaker-max-cooldown 2m` |
| B（快速候选） | 3 | 2s | 20s | `-breaker-failure-threshold 3 -breaker-base-cooldown 2s -breaker-max-cooldown 20s` |

每个候选各起一个 Gateway 副本（`-replica-id gw-local-default` / `gw-local-fast`），复用同一份证书与同一个 Agent，切换候选时只重启 Gateway 进程（Agent 因隧道对端消失后自动重连；一次因 Registry 名册推送延迟改为直接重启 Agent 进程，日志见 `logs/agent-full.log`，保存在 `dist/`）。

## 负载生成器

`loadgen`（Go，`net/http` 标准库，独立 `go.mod`，未提交进本仓库，只活在本次会话的 scratchpad 里）向 `POST /v1/chat/completions` 发送短合成 prompt（5 条固定句子轮询，`max_tokens=48`），按 `-stream-ratio` 混合流式/非流式，记录每条请求的时延、TTFT（流式，取首个 `data:` 行的时刻）、HTTP 状态、OpenAI 风格错误码与成功与否；API Key 只用于 Authorization 头，从不写入 CSV 或日志。第一轮（候选 A 的硬饱和与断连场景）没有限速，暴露出一个真实问题（见下），随后为全部候选加了 `-min-interval 150ms`（同一个 worker 两次请求之间的最小间隔）复现，本报告的主表格均取自**限速后**的数据；未限速的原始数据作为附录保留。

## 场景与结果（限速数据）

四类场景对两个候选各跑一次，请求期限（客户端超时）：稳定/饱和 20s，断连场景 15s。

### 1. 稳定负载

并发 2，流式占比 0.4。

| 候选 | 时长 | 样本数 | 成功率 | 时延 p50/p95 | TTFT p50/p95 | 熔断跳闸 |
| --- | --- | --- | --- | --- | --- | --- |
| A（5/5s/2m） | 4min | 345 | 99.71%（1 次真实 `upstream_error`，孤立未连续） | 1388/1535ms | 797/953ms | 0 |
| B（3/2s/20s） | 3min | 256 | 100% | 1400/1551ms | 806/937ms | 0 |

两候选表现一致，说明这条基线与熔断参数无关——预期之内。

### 2. 中度饱和（略高于 (1) 观测到的串行容量）

并发 6，2min，流式占比 0.4。真实单 Ollama 实例在此并发下开始排队但不拒绝。

| 候选 | 样本数 | 成功率 | 时延 p50/p95 | TTFT p50/p95 | 熔断跳闸 |
| --- | --- | --- | --- | --- | --- |
| A | 171 | 100% | 4368/4638ms | 3706/3974ms | 0 |
| B | 149 | 99.33%（1 次 `backpressure`） | 5054/5381ms | 4344/4703ms | 0 |

排队让时延涨了约 3 倍，但两候选都没有熔断跳闸——符合「排队不是节点坏了」的调度器契约。

### 3. 硬饱和（远超观测容量）

并发 14，2min，流式占比 0.4，`-min-interval 150ms`。

| 候选 | 样本数 | 成功率 | 成功请求时延 p50/p95 | `backpressure` 计数 | 熔断跳闸 |
| --- | --- | --- | --- | --- | --- |
| A | 4945 | 3.48%（172 成功） | 5773/6299ms | 4773 | **0** |
| B | 4923 | 3.11%（153 成功） | 6583/7115ms | 4770 | **0** |

两个候选在限速客户端下的硬饱和里熔断跳闸计数都是 0——`gateway_scheduler_dispatches_total{result="backpressure"}` 相应增长，`ErrorBackpressure` 确认不计入熔断连续失败，这一条调度器契约在真实单节点饱和下成立。

**重要发现（附录，仅候选 A）**：同样的硬饱和场景，如果负载生成器不做任何速率限制（连续失败后立刻重试，零间隔），单节点集群在 3 分钟内发出 1,101,024 次请求，其中仅 38 次成功，`backpressure` 592,970 次、`model_not_found`（`scheduler.ErrNoCapableNode`）428,536 次，另有 613 次真实 `connection_failed`、44 次真实 `upstream_error`——这两类是熔断计入类型，累计触发了 **28 次真实熔断跳闸**，尽管后端从未真正故障，只是被无退避的请求风暴在 OS 层面顶穿了连接队列。跳闸期间，由于是单节点集群，该 runtime 的每一条请求都在 `candidates()` 阶段就被排除，直接返回 `model_not_found`（不再有机会命中 `backpressure`），这解释了两种错误码此消彼长的比例。这不是熔断器的缺陷，而是「无退避重试的调用方 + 单节点集群」组合下的真实副作用：现实中很少有调用方会以这种零间隔方式打爆一个端点，但如果有（例如一个失控的重试循环），当前默认参数确实会被这种自伤式流量真实触发。原始 CSV/日志见 `dist/p10/2026-09-13/breaker-calibration/`（`candidateA_saturation_hard_unpaced.csv.gz`、`gateway-candidateA.log.gz`）。

### 4. 后端断连与恢复

并发 3，总时长 220s，流式占比 0.4，`-min-interval 150ms`。中途用 `killall Ollama` 完全退出真实 Ollama.app（而非仅杀其托管的 `ollama serve` 子进程——该子进程由 app 的监督进程在约 2s 内自动拉起，无法制造持续故障），确认 `curl :11434/api/tags` 连接被拒绝，等待一段真实故障窗口后用 `open -a Ollama` 重新拉起，直到模型重新可用。

| 候选 | 故障窗口 | 断连后首次观测到跳闸 | 跳闸期间总跳闸数 | 断连期间 `breaker_open` | 恢复所需时间（后端恢复到 gauge 归零） | 整个 220s 窗口成功率 |
| --- | --- | --- | --- | --- | --- | --- |
| A（5/5s/2m） | 60s（10:56:07–10:57:07 UTC） | 断连后 ≤20s（采样粒度 20s） | 33→38（+5） | 全程为 1 | 恢复后 20–40s 之间（采样粒度 20s） | 8.98%（177/1971） |
| B（3/2s/20s） | 45s（11:09:07–11:09:52 UTC） | 断连后 ≤15s（采样粒度 15s） | 0→6（+6） | 全程为 1 | 恢复后 15–30s 之间（采样粒度 15s） | 12.96%（204/1574） |

断连恢复后，成功请求时延：A p50/p95=2145/2301ms、TTFT p50/p95=1503/1654ms；B p50/p95=2183/2457ms、TTFT p50/p95=1568/1746ms（均混合了断连前/恢复后的健康流量）。跳闸后两个候选都**只跳闸一轮就保持稳定 open**，恢复后**都没有再次跳闸**（trips_total 在整段恢复观测窗口内不再增长）——没有出现来回抖动。

**跳闸计数的一个真实现象**：两个候选在刚进入故障窗口时都记录了明显多于 1 次的跳闸（A 是 +5，B 是 +6），而不是教科书式的「一次连续失败达标 → 一次跳闸」。原因在 `scheduler/breaker.go`：跳闸后 `consecutiveFailures` 不会清零，只会在下一次成功时清零；断连期间每一次冷却到期后的探测失败都会立刻让 `consecutiveFailures >= threshold` 再次成立并重新跳闸（`record()` 对「已经 open」和「刚刚 open」不做区分）。并发为 3 意味着冷却到期的瞬间可能有多个 worker 几乎同时发起探测，各自独立失败、各自计一次跳闸——这是 README「没有做教科书式的单飞探测」这一有意设计选择的可观测代价：`gateway_scheduler_breaker_trips_total` 应读作「探测失败发生的次数」，不是「独立故障事件数」。

单节点集群还有一个值得记录的连带效应：熔断一旦 open，这个 runtime 的**全部**请求都会在 `candidates()` 阶段被排除、直接 404 `model_not_found`——因为没有第二个节点可以接住流量。多节点机群里，一个节点熔断只是让调度器换到另一个候选；单节点或稀疏机群里，一次熔断等于这个 runtime 的整体不可用直到冷却结束。这不是熔断器的缺陷，但意味着熔断参数对薄机群的影响权重要远大于对厚机群的影响。

## 候选对比

| 维度 | A（默认 5/5s/2m） | B（快速 3/2s/20s） |
| --- | --- | --- |
| 稳定/中度饱和/限速硬饱和下的误跳闸 | 均为 0 | 均为 0 |
| 无退避风暴下的误跳闸风险（附录场景，仅测了 A） | 28 次/3min（真实发生） | 未测——理论上阈值更低、冷却更短，同等风暴下大概率跳闸更快、更频繁，代价方向未知，需要专门测才能下结论 |
| 断连后进入熔断的时间 | ≤20s | ≤15s（更快） |
| 单次故障窗口内的跳闸次数 | +5 | +6（并发探测放大效应，两者量级相近） |
| 恢复所需时间（真实模型冷启动 + 冷却窗口） | 20–40s | 15–30s（略快） |
| 长时间持续故障下的最大冷却间隔 | 2m（本次故障窗口未触达该上限） | 20s（本次同样未触达；意味着一旦真的持续故障更久，B 会比 A 频繁得多地重新探测一个仍然故障的后端） |

本次故障窗口（45–60s）远短于两个候选任一个的 `MaxCooldown`，因此这次实验**没有观测到**「更短的 `MaxCooldown` 在长时间持续故障下会造成更多无意义探测」这一权衡的真实代价，只观测到了「更低阈值 + 更短起始冷却」在短故障下略微更快进入与退出熔断的收益。要给出关于 `MaxCooldown` 的建议，需要一次远超 2 分钟的真实故障窗口，本轮时间预算内未安排。

## 建议（供维护者决策，非已应用的变更）

1. **保留当前默认值（5 次 / 5s / 2m）不变。** 本轮三类正常/饱和场景下两个候选表现一致且均为零误跳闸，默认值在真实单节点环境里没有表现出问题；候选 B 在断连恢复速度上只有个位数秒的优势，不足以抵消它在更短 `MaxCooldown` 下对长时间持续故障可能更频繁地做无意义探测的未知代价——这一权衡本轮未覆盖。
2. **把「无退避重试风暴可能真实触发熔断」记录为已知边界情况**，而不是据此收紧或放宽阈值：这是调用方行为问题，不是后端健康问题，且本轮只针对候选 A 观测到，未做候选间对比。建议 Gateway README 在熔断小节补充这一现象作为运维排查线索（本记录已提供原始数据），是否需要代码层面的调用方限速是独立的话题。
3. **候选 B 或类似「更激进」的参数只在多节点/厚机群场景下才可能划算**：厚机群中一次误跳闸的代价是「少一个候选」，薄/单节点机群中的代价是「这个 runtime 整体不可用到冷却结束」。本轮只测了单节点，不能代表多节点场景下的成本收益。
4. **后续如要进一步校准**，建议至少补充：(a) 一次超过 2 分钟的真实持续故障窗口，量化 `MaxCooldown` 差异的真实代价；(b) 多节点机群下同一组场景，验证「厚机群里单点熔断只是换节点」的假设；(c) 候选 B 在无退避风暴下的表现，而不仅仅是候选 A。

## 质量门禁（Part 1 代码改动）

```
gofmt -l ./service ./api   # 无输出
go vet ./...                # 通过
go build ./...              # 通过（四个 Go 服务入口）
go test ./service/aiServeWeaveGateway/...   # 全部通过
```

改动文件：`service/aiServeWeaveGateway/main.go`（新增三个 flag 并接入 `scheduler.Config`）、`service/aiServeWeaveGateway/README.md`（同步熔断小节文档）。未修改 `service/aiServeWeaveGateway/scheduler/breaker.go` 的默认常量。完整 `go test ./...` 与 `-race ./service/...` 因与本机并行运行的 24h 长稳（`dist/p10/2026-09-13/soak-24h/`，本轮未触碰）抢占资源，本轮未重复跑；Part 1 改动本身是纯增量、零值不改变行为，风险面很小。

## 证据

本目录（随本仓库提交）：

- `loadgen/candidate{A,B}_{stable,saturation_moderate,saturation_hard,disconnect}.csv` —— 限速负载生成器的逐请求原始记录，即本报告全部表格的数据来源。
- `metrics/candidate{A,B}.log` —— 每个场景前后的 `/metrics` 快照（`breaker`/`dispatches_total` 相关行）。
- `metrics/candidate{A,B}_disconnect_timeline.log` —— 断连场景每 15–20s 一次的 `/metrics` 快照全量时间线，含 kill/relaunch 时间戳。

`dist/p10/2026-09-13/breaker-calibration/`（本机保留，`dist/` 已被 gitignore）：

- 上述文件的完整版本，另加未限速的附录场景（`candidateA_saturation_hard_unpaced.csv.gz`、`candidateA_disconnect_unpaced.csv.gz`、`candidateA_disconnect_unpaced_timeline.log`）。
- `logs/gateway-candidateA.log.gz`、`logs/gateway-candidateB.log`、`logs/agent-full.log` —— Gateway/Agent 结构化日志（JSON，含脱敏 `request_id`），已核对不含 API Key 或 prompt 原文。
- `logs/candidate{A,B}_disconnect_loadgen.stderr` —— 负载生成器汇总行。

本轮运行未触碰、未终止同时段并行运行的 24h 长稳进程（`dist/p10/2026-09-13/soak-24h/`）。
