# 独立 Tunnel Gateway 与事件基础设施评估

本文档交付根 STATUS.md「P2：后续扩展」的一项："根据实际规模评估独立 Tunnel Gateway 与事件基础设施，避免在可靠性闭环完成前增加不必要的服务拆分"。

范围边界：**本项以评估为主，不含新代码或新测试**，与 [A04](2026-09-15-a04-direct-mode-boundary-design.md)/[A05](2026-09-15-a05-scheduling-extension-breakdown-design.md) 同一先例——STATUS.md 用的动词是"评估"，不是"实现"，且评估的前提条件（"避免在可靠性闭环完成前拆分"）本身要求先核实可靠性闭环是否已完成。凡属阅读代码得出的结论标注文件:行号；仅在 README/STATUS 散文中出现、找不到代码依据的一律计入第六节缺口清单，不采信为现状。

## 一、前提核实：可靠性闭环已完成，但"实际规模"尚不存在

STATUS.md 的措辞把"评估拆分"和"可靠性闭环完成"绑定为先后关系。M1（J01–J08）在本文档写作时已全部勾选完成（STATUS.md:41-58），M2/M3 的多数项也已完成，因此"闭环完成前"这道前置门槛已经跨过，评估本身现在是合时宜的。

但"根据实际规模评估"这半句要求的是真实负载数据，而 [A01](2026-09-14-a01-scope-reliability-design.md) 已经把这件事的现状讲得很清楚：

- "单 Gateway 副本或整个机群可接受的节点（Agent）总数、单节点可承载的并发推理请求数，代码里都没有强制上限，也没有真实多节点规模下的实测数据"（A01 §37）；
- `node_total_slots=32` 是"未经生产验证的占位默认值"（A01 §192）；
- "没有'节点数/机群规模'上限"（A01 §193）；
- [A02](2026-09-14-a02-disaster-recovery-key-rotation-design.md) 与 P07 均确认"当前无生产租户阶段"——这个项目至今没有跑过真实生产流量。

**结论：不存在可供本文档引用的"实际规模"数据。** 这不是评估的失败，而是评估本身必须如实报告的第一个发现——STATUS.md 要求"根据实际规模"评估，而实际规模的证据链在 A01 已经查过一遍且为空。因此本文档不能、也不会编造一个吞吐量或节点数门槛来证明拆分"划算"或"不划算"；能做的是核实当前架构的耦合程度、指出没有拆分时系统如何解决了拆分本来要解决的问题（第三节）、并给出拆分被真正需要时应当观察的信号（第四节），而不是给出一个日期或数字。

## 二、现状核实：Tunnel Gateway 今天不是独立服务，是同进程内的一个监听器

`service/aiServeWeaveGateway/main.go` 一个二进制里挂了四个可独立配置地址的监听器：`-addr`（OpenAI/Anthropic/Ollama 前门 HTTP API，main.go:71,440）、`-tunnel-addr`（Agent mTLS 隧道，main.go:80）、`-admin-addr`（只读机群清单，main.go:120,486）、`-model-pull-addr`（模型拉取触发写操作，main.go:122-123,521）。四个监听器**端口可分离，但进程和内存不可分离**：

1. **`Scheduler` 直接持有 `*tunnelserver.Server` 指针，不经 RPC。** `scheduler.Scheduler`（`scheduler/scheduler.go:69-70`）的 `server` 字段就是 `*tunnelserver.Server`；`Chat`/`Embed`/`ChatStream` 等方法最终都以 `s.server.Runtime(nodeID, runtimeID).Chat/Embed/...(...)` 收尾（[A04](2026-09-15-a04-direct-mode-boundary-design.md) §1.2 已核实并给出行号），这是一次进程内函数调用，不是网络调用。这意味着"HTTP 前门"和"隧道终结"这两个逻辑角色今天共享同一份 Go 调用栈，没有序列化/反序列化开销，也没有网络故障模式。

2. **`jobStore` 是进程内内存表，没有走任何 RPC 或消息队列。** `jobStore`（`httpapi/jobstore.go:212-241`）被 `httpapi` 包内至少四个文件直接读写（`jobs.go`、`jobrecover.go`、`jobsync.go`、`jobpersist.go`，见本次 codegraph 核实的 blast-radius 结果），持有 `sync.Mutex` 保护的 map，doc comment 明确写着"每个方法交还的都是副本"（`jobstore.go:210-211`）——这是单进程内的读写隔离手段，不是分布式一致性手段。

3. **节点心跳/健康/槽位状态全部只存在于持有该隧道连接的那个副本进程里。** `tunnelserver.Server.Nodes()` 遍历的候选节点数据来自 Agent 在 Control 流上报的心跳（[A04](2026-09-15-a04-direct-mode-boundary-design.md) §1.3），这份数据结构性地只存在于接住这条 Control 流的那个 Gateway 进程的内存里，从未被序列化发给别的进程。

把 Tunnel Gateway 拆成独立服务，意味着上述三条耦合全部要改成跨进程边界：Scheduler 要么搬进 Tunnel Gateway 进程（那就不是"拆分 HTTP 前门和隧道终结"，而是把 HTTP 前门本身也一起拆过去），要么 Scheduler 留在 API Gateway 进程、通过新的 RPC 层调用 Tunnel Gateway——这条新 RPC 层要承载今天 `runtime.InferenceRuntime`/`WorkflowRuntime` 接口的全部语义（Chat/Embed/ChatStream/Transcribe/Rerank/SubmitWorkflow/…），本质上是把"Agent↔Gateway"这层隧道协议原样再抄一遍到"API Gateway↔Tunnel Gateway"这一层，工作量不小于当年整个隧道协议的设计与实现。

## 三、为什么今天没有独立的事件基础设施——现有设计已经绕开了这个需求

STATUS.md 把"独立 Tunnel Gateway"和"事件基础设施"并列提出，这不是巧合：**如果 Tunnel Gateway 与 API Gateway 是两类不同的服务、各自可以有不同数量的副本，那么"哪个 API Gateway 副本该问哪个 Tunnel Gateway 副本要某个节点的推理事件"就成了一个需要显式解决的路由问题**——这正是消息队列/事件总线通常被引入解决的问题。但核实结果是：当前单进程架构下，这个问题从未出现过,因为架构已经用另一种方式避开了它,而不是"忘了做"：

1. **Agent 对每一个已知副本维持独立的 Control+Serve 连接,不是只连一个"主"副本。** `tunnel.Roster`（`service/aiServeWeaveAgent/tunnel/roster.go:53-66`）记录的是一整份副本清单（默认上限 16,`roster.go:71-73`),`tunnel.Manager`（隧道 README 已有文档,STATUS.md:25"多副本连接与槽池"）对清单里**每一个**副本都独立拨号、独立维持 Control 流和 Serve 槽池。也就是说,今天已经是"N 个 Gateway 副本,每个都与同一批 Agent 有自己独立的隧道连接",而不是"一个副本连着 Agent,其余副本靠某种方式转发"。

2. **跨副本访问同一个 job 靠的是"另开一条独立隧道连接直接问节点",不是"问第一个副本要数据"。** J06 的恢复机制（STATUS.md:54）核心是:job 的路由绑定（`node_id`/`runtime_id`）持久化进控制面 MySQL,任何副本重启或收到请求后,通过 `GET /internal/v1/jobs/active`（`service/aiServeWeaveControlPlane/internal/handler/routes.go:224-225`）把绑定关系拉回来,然后用**自己那份独立的隧道连接**直接问对应节点要最新状态——不需要问撞见这个 job 的那个副本"你那边现在什么情况",因为每个副本本来就有能力独立问节点本身。本次会话刚交付的跨副本产物恢复（STATUS.md:124「Gateway 已有多副本连接能力」一条）同理:终态 job 的产物元数据在控制面持久化,一个副本本地未命中时直接问控制面要 `StorageKey`/`NodeID`/`RuntimeID`,而不是问另一个副本。

3. **唯一真正跨副本传播的状态是"哪些副本存在"和"哪些节点/Key 被吊销",而这两者已经各自有专用机制,不需要通用事件总线。** Registry 的 `GatewayDirectory` 广播副本名册给 Agent（S03,STATUS.md:72),控制面的 P06 用 Redis `Lua INCR + PUBLISH` 推进吊销 generation、Gateway 侧 2 秒心跳长轮询感知（STATUS.md:91)。这两条路径都是"针对一种特定的、低频的跨副本事实"定制的窄接口,不是给任意事件建的通用总线——`service/aiServeWeaveGateway/ratelimit/redis.go` 是 Gateway 唯一直接碰 Redis 的地方,用于跨副本配额计数,同样是窄接口而非事件基础设施。

**结论:今天不存在事件基础设施,不是遗漏,而是"每个副本独立拥有到每个 Agent 的连接"这个设计选择的直接后果——它把"如何在多个服务实例间传播推理事件"这个问题,替换成了"如何让任何一个实例独立获得同样的数据",后者靠的是隧道多连接 + 控制面持久化,不需要引入消息队列。** 如果 Tunnel Gateway 与 API Gateway 拆成两类服务,这个替换不再成立——API Gateway 副本不再天然拥有到 Agent 的连接,届时才第一次真正需要"事件基础设施"这个概念,而不是现在。

## 四、评估结论与触发信号

**结论:不建议现在拆分。** 理由不是"规模太小",而是第一节已确认的"没有规模证据"——在没有真实生产负载的前提下拆分,是在没有问题的地方引入问题(第二节的耦合成本 + 第三节指出的、拆分后才第一次出现的事件基础设施需求),这正是 STATUS.md 本条目本身写的"避免……增加不必要的服务拆分"。

拆分决策不应该按日期排期,而应该按下列**可观测信号**触发——这些信号今天已经有对应指标可以直接查询,不需要新增采集代码:

| 信号 | 对应现有指标 | 拆分动机 |
| --- | --- | --- |
| 单副本连接节点数持续逼近文件描述符或内存上限 | `tunnel_server_connected_nodes`（`tunnelserver/metrics.go:57`) | Tunnel 终结本身需要独立扩容,与 HTTP 前门的扩容需求脱钩 |
| HTTP 前门的请求量增长速率明显快于节点/隧道连接数增长速率(两者目前 1:1 绑在一个副本里,想单独多开 HTTP 副本目前只能连带多开一份隧道终结能力) | `gateway_http_requests_total` 对比 `tunnel_server_connected_nodes` | 两个角色的资源需求出现分化,合并部署开始浪费资源 |
| 调度队列持续积压,且排查后发现瓶颈在 HTTP 处理而非节点容量 | `gateway_scheduler_queue_depth`/`gateway_scheduler_queue_wait_seconds` | 说明 HTTP 层是瓶颈,拆分能让它独立扩容而不必连带扩容隧道终结 |
| 隧道协议发布(新 Operation、新字段)与 HTTP 前门发布(新 API 端点)的变更节奏出现冲突,同一次部署窗口两者互相阻塞 | 无对应指标,是运维流程观察 | 拆分能让两条发布线独立灰度/回滚 |
| 单节点槽位争用或分发延迟异常,且与 HTTP 侧请求量无关 | `tunnel_server_slots_total`/`tunnel_server_dispatch_duration_seconds` | 排除 HTTP 前门影响后,若隧道终结本身是瓶颈,先看是否是 `node_total_slots`（A01 已知占位默认值)需要调,不必然直接导向拆分 |

以上五条里,前四条任意一条持续成立(不是瞬时尖峰),才构成重新评估的理由;第五条更可能先靠调参数解决,不必然导向拆分。**核实结果是:上述指标全部已经存在,但本次评估没有观察到任何一条被触发的证据**——项目至今没有生产租户(A02),这些指标从未在真实负载下运行过。

## 五、若未来触发拆分:设计草图(不在本项范围内交付)

本节只给方向,不做实现设计,供第四节信号被触发时作为起点,避免届时从零开始:

1. **RPC 边界选型**:Tunnel Gateway 需要对外暴露与今天 `runtime.InferenceRuntime`/`WorkflowRuntime` 等价的调用面。最小改动路径是让 `scheduler.Scheduler` 依赖的不再是 `*tunnelserver.Server` 具体类型,而是一个接口(`tunnelserver.NodeRuntime` 已经实现了 `runtime.InferenceRuntime`,见 A04 §1.3——这个接口边界本来就存在,缺的只是让它跨进程),新增一层 gRPC 客户端实现同一接口,复用 `api/proto/tunnel/v1` 已有的消息形状而非发明新协议。
2. **候选发现与心跳如何跨进程传播**:API Gateway 副本需要知道"哪些 Tunnel Gateway 副本连着哪些节点、这些节点当前的 `runtime.Snapshot`",这正是第三节指出的、拆分后第一次真正需要的东西。两个方向可选:(a) Tunnel Gateway 把心跳数据写入一个共享存储(Redis 或控制面),API Gateway 轮询/订阅——这就是"事件基础设施"第一次成为必需品的地方;(b) 每个 API Gateway 副本对每个 Tunnel Gateway 副本维持一条长连接直接拉取,类比今天 Agent 对每个 Gateway 副本维持连接的模式,把"N Agent × M Gateway"的网状连接模式平移成"N Tunnel Gateway × K API Gateway"——这条路径不需要新引入消息队列技术栈,但网状连接数是 N×K,需要评估是否可控。两个方向的权衡(强一致性/新增技术栈 vs 连接数增长)是拆分被触发时需要做的架构决策,本文档不预先选定。
3. **AGENTS.md 依赖红线的约束**:新增事件基础设施(Kafka/NATS/Redis Streams 等)会撞上 AGENTS.md"Agent 与 Registry 的直接依赖只有 gRPC、protobuf 与 `coder/websocket`""go-redis 是经过评估的例外"的既有红线——引入新的消息队列技术栈需要同等级别的评估和记录,不能顺手加进去;上述方向 (b) 刻意选择复用已有的 gRPC/隧道协议模式,是为了不必现在就做这个评估。
4. **迁移路径**:不应该是一次性切换,应该先让 API Gateway 与 Tunnel Gateway 可以合并部署(同进程,今天已经是这样)也可以分离部署(不同进程,通过新 RPC 边界通信),用一个配置开关切换,验证两种拓扑行为一致后再在生产环境分离,类比 P02/P03 路由/模板"文件模式与控制面模式共存"的既有迁移先例。

## 六、已知缺口清单

1. **没有真实规模数据,详见第一节**——这是本次评估最核心的发现,不是缺口而是结论本身,但仍在此列出以避免被误读为"评估者没找到数据"。
2. **HTTP 前门与隧道终结各自的真实资源画像(CPU/内存/连接数 per unit of throughput)未测量**——第四节的信号表给出的是"该看哪个指标",不是"阈值是多少",因为没有真实负载,任何具体阈值都会是编造的。
3. **第五节的 RPC 边界与心跳传播方案均为方向性草图,未经原型验证**——两个候选方向(共享存储 vs 网状连接)都只做了定性权衡,没有做过任何 spike 或性能测试。
4. **与 A05(调度扩展拆分)的交叉未合并**——A05 提到的"延迟评分""会话亲和"等调度扩展如果未来实现,其数据来源(第五节方向 (b) 的网状连接,还是方向 (a) 的共享存储)会影响这些扩展该建在哪一层,本文档不展开。
5. **"事件基础设施"在 STATUS.md 原文里没有更具体的定义**——全仓库搜索确认这个短语只出现在 STATUS.md:126 这一处(见本文档写作前的核实),本文档第三节的解读("拆分后才需要的跨进程事件传播机制")是基于架构推断,不是转述了某处已有的更详细设计意图。

## 七、与 STATUS.md 的关系

本文档完成本条目要求的"评估"——核实可靠性闭环已完成(第一节前半)、核实"实际规模"证据不存在因而不编造门槛数字(第一节后半)、核实当前架构的进程内耦合程度(第二节)、解释现有设计如何在没有事件基础设施的情况下解决了跨副本一致性问题(第三节)、给出拆分应由信号而非日期触发的具体指标(第四节)、以及信号被触发时的后续设计起点(第五节)。**本项不实现任何拆分,不新增代码或测试**,结论是"现在不拆",与 [A04](2026-09-15-a04-direct-mode-boundary-design.md)/[A05](2026-09-15-a05-scheduling-extension-breakdown-design.md) 同一先例;STATUS.md 本条目据此勾选为完成并链接回本文档,措辞维持"避免不必要拆分"的既有判断,不改变任何服务的部署形态。
