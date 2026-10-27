# A04 Direct 模式边界核实

本文档交付根 STATUS.md「规划补充」A04：对照代码确认可交付的 Direct 路径及与 Tunnel 的差异；定义地址允许列表、后端凭据归属、SSRF 防护与验收。

范围边界：**本项以设计文档为主，不含新代码或新测试**，与 [A01](2026-09-14-a01-scope-reliability-design.md) 同一先例。原因和 A01 一样——STATUS.md 用的动词是"核实"（对照代码确认现状）与"定义"（划出后续实现该走的边界），不是"实现"；而下文第一节的核实结果是 Direct 在 Go 代码里**完全不存在**（零 Deployment 类型、零地址允许列表、零 SSRF 防护、零生产路径 HTTP 客户端），要把它做成能跑的功能需要新设计出候选发现/健康模型、凭据落地、SSRF 守卫和 Deployment схема 四块新机制（见第七节），工作量是一个独立的实现任务，不是可以顺手在核实过程中写完的补丁。凡属阅读代码得出的结论标注文件:行号；凡属仅在 README/STATUS 散文中出现、找不到代码依据的一律计入第八节缺口清单，不采信为现状。

## 一、现状盘点：Direct 在代码里不存在

### 1.1 文档层面的 Direct 描述（仅设计意图，非实现证据）

README.md 三处提到 Direct：架构图（README.md:38-41）画了一条 `Gateway → Direct → 可达推理后端` 的箭头；节点接入模式表（README.md:131-136）把 Direct 定义为"有内网或公网可达地址的 GPU 服务器，Gateway 直接调用节点推理服务"，并声明"Direct 只访问受配置与权限约束的后端地址，不能退化为任意 HTTP 代理"（README.md:136）；ComfyUI 接入范围（README.md:245-248）把"通过 Direct 模式访问的 ComfyUI 服务器"列为目标而非已交付项。STATUS.md 已经在 A04 词条本身明确否定了这些图的证明力："目标架构中的 Direct 箭头不作为实现证明"（STATUS.md:102）——本节的任务就是把这句话坐实成具体的代码事实。

### 1.2 Go 代码层面：零命中

- 不存在 `Deployment` 类型：`grep -rn "type Deployment" common/modelroute service/aiServeWeaveGateway` 无匹配。`modelroute.Target`（`common/modelroute/modelroute.go:30-35`）只有 `RuntimeModel`、`NodeSelector`、`Priority`、`Weight` 四个字段，没有地址、没有连接模式；其 Gateway 侧别名 `routing.Target = modelroute.Target`（`service/aiServeWeaveGateway/routing/routing.go`）同样没有扩展。
- 不存在 Direct 模式枚举、config 字段或 CLI flag：全仓库 Go 源码搜索 "direct"（大小写不敏感）只命中三类噪音——`direction`/`bidirectional`（`tunnel/metrics.go`、`tunnelserver/metrics.go`、`httpapi/metrics.go` 里 inbound/outbound 帧字节计数器的标签名，与 Direct 模式无关）、`directory`（ComfyUI 客户端里的文件路径）、以及英文注释里作副词用的 "directly"（例如 `tunnel.pb.go` 里 `Endpoint` 字段的 doc comment "must be directly dialable by the Agent"，说的是 Agent 拨 Gateway 副本的隧道控制连接，方向和语义都与"Gateway 直拨后端"相反）。没有任何一处是 Direct 模式的功能代码。
- Gateway 生产派发路径没有面向后端的 `net/http` 调用：`grep -rn "http.Client\|http.Get\|http.Post\|http.NewRequest" service/aiServeWeaveGateway/scheduler service/aiServeWeaveGateway/tunnelserver service/aiServeWeaveGateway/httpapi` 排除 `_test.go` 后零命中。唯一使用 `common/runtime/ollama` 的 Gateway 代码是 `e2e/faultinjection_test.go`，用于联调测试起一个假后端，不是生产派发代码。
- 请求实际怎么走：`httpapi` 的 chat/embed 处理器调 `Scheduler.Chat`/`Embed`/`ChatStream`（`scheduler/scheduler.go:151,176,207`），三者都以 `s.server.Runtime(c.NodeID, c.RuntimeID).Chat/Embed/ChatStream(...)` 收尾（`scheduler.go:159,184,215`），`s.server` 的类型是 `*tunnelserver.Server`（`scheduler.go:59`）。`Server.Runtime(nodeID, runtimeID)` 返回 `*tunnelserver.NodeRuntime`（`tunnelserver/node_runtime.go:43`），它经隧道把调用转成 gRPC 帧发给已连接的 Agent 副本——今天**每一次**推理请求，不论候选是谁，都会走到这里，没有任何分支会绕开隧道去拨一个 HTTP 地址。

### 1.3 唯一可复用的同构点：`NodeRuntime` 实现的是接口，不是"两种模式都能选"

`aiServeWeaveAgent/tunnel/README.md:69,85` 声称"隧道与 Direct 模式在 Gateway 侧共用同一个 `runtime.InferenceRuntime` / `WorkflowRuntime` 接口……调度器因此不感知 Direct 与 Tunnel 的差别"。这句话在**接口层**是真的，且有代码坐实：`tunnelserver.NodeRuntime` 的 doc comment 写得很直白——"the Gateway's scheduler and API layer program against the same interfaces the Agent's adapters implement, so a request travelling over a tunnel and one served in-process are the same shape of call"（`node_runtime.go:14-20`），并用编译期断言 `var _ runtime.InferenceRuntime = (*NodeRuntime)(nil)` 固定这一点（`node_runtime.go:35-37`）。这意味着：如果未来要交付 Direct，正确的落点是新写一个同样实现 `runtime.InferenceRuntime`/`WorkflowRuntime` 的类型（大概率直接复用 `common/runtime/ollama`、`vllm`、`sglang`、`comfyui` 四个包——它们本来就是 Agent 与 Gateway"共用"的抽象，见 AGENTS.md 代码地图），调度器改动应该很小。

**但这句"两侧同构"的声明止步于接口调用这一层，没有覆盖候选发现和健康评分这一层，这是本次核实发现的关键缺口**：`Scheduler.pickBy`（`scheduler.go:397-454`）遍历候选节点的唯一数据源是 `s.server.Nodes()`（`tunnelserver.Server.Nodes()`），每个 `node` 的 `Live`/`Draining`/`Maintenance`/`IdleSlots`/`Runtimes`（后者是 `[]runtime.Snapshot`）全部来自 Agent 在 Control 流上的心跳上报——这条数据完全依赖"有一个 Agent 连着隧道、按 README 已实现的控制流协议上报健康和能力"。Direct 目标按定义没有 Agent，所以`pickBy` 今天遍历的这份候选列表结构性地不包含、也不可能包含任何 Direct 目标：不是"漏了一个 if 分支"，而是"这份候选列表的数据来源本身就是隧道心跳"。要让 Direct 目标参与调度，必须先回答"Direct 目标的 `Live`/健康状态/空闲槽位由谁上报、怎么上报"——这需要 Gateway 自己跑一份类似 Agent `localdiscovery`/`runtime.Manager` 的探测循环，直接对着 Direct 后端做健康检查，产出等价的 `runtime.Snapshot`，再想办法喂给 `pickBy` 或它的等价物。这不是本文档要设计的实现细节（属于第七节列出的后续实现任务），但核实阶段必须把这一层的缺失讲清楚，否则"两侧同构"这句话会被误读成"候选发现也是现成的"。

## 二、Direct 与 Tunnel 的差异定义（目标形态，非承诺）

| 维度 | Tunnel（已实现） | Direct（待定义，本文档划边界） |
| --- | --- | --- |
| 连接建立方 | Agent 主动出站，mTLS，向 Registry 换证 | 无连接建立环节——Gateway 按需对一个已知地址发起普通请求 |
| 候选健康/容量来源 | Agent Control 流心跳上报 `runtime.Snapshot`（`tunnelserver.Server.Nodes()`） | **未定义**：需要 Gateway 自己探测（复用 `common/runtime` 的 Discover/健康检查），或接受"无实时健康、只靠请求失败触发熔断"的降级模型 |
| 后端凭据归属 | Agent 本地解出（见第四节），从不过隧道 | Gateway 必须直接持有可用凭据——结构性差异，见第四节 |
| 地址来源与约束 | Agent 本地 `runtime.Config.BaseURL`，运维配置在 Agent 主机上，Gateway 从不知道具体地址，只知道 `node_id`/`runtime_id` | Gateway 必须知道并主动拨通一个具体地址——地址从哪来（控制面发布的 Deployment，还是 Gateway 本地配置）以及谁能改它，直接决定攻击面，见第三节 |
| SSRF/任意代理防护 | 由 Agent 的 `AllowedRuntimes` 白名单 + "只传运行时语义不传 HTTP" 的协议设计保证（`tunnel/dispatch.go:24-27`） | **完全空白**，见第五节 |
| 取消/超时 | 帧级取消，`request_id` 全链路透传（隧道协议原生支持） | 需要用普通 HTTP 的 `context` 取消 + 超时,语义上可对齐,但要单独实现,不能假设隧道那套帧级取消可以直接搬过来 |
| 流式响应 | gRPC 双向流,一槽一请求,天然背压 | 需要用 HTTP chunked/SSE 或后端原生流式协议单独实现,背压保证需要重新论证(AGENTS.md 安全红线"任何一跳都不得无界缓冲"同样适用于 Direct) |
| ComfyUI WebSocket 事件转发 | Agent 代理 WS 事件(README.md:127) | Gateway 需要自己维护到 ComfyUI 的 WS 连接,是一块全新代码,不是"复用 Agent 那份逻辑"能解决的(部署位置不同) |

## 三、地址允许列表设计

**所有权原则,对齐 AGENTS.md 安全红线的对等要求。** AGENTS.md 明确写着"`runtime_id` 必须命中 Agent 本地白名单才执行——即使 Gateway 被攻破也不能让 Agent 访问未声明地址"。这条红线的落点是`tunnel/dispatch.go`里`DispatchConfig.AllowedRuntimes`（`dispatch.go:56-60`,注释见`dispatch.go:24-27`:"A compromised replica must not be able to turn this Agent into an internal port scanner"）和`tunnel/control.go:382-391`的`runtimeAllowed`——关键设计是这份白名单**只能来自 Agent 进程自己的本地配置**（`main.go:169-172`解析),不接受控制面或 Gateway 远程下发,这样"即使上游被攻破"这句话才成立。

Direct 模式需要一份**结构对等**但**语义相反默认值**的白名单：

1. **所有权同样必须落在 Gateway 本地**,不接受控制面远程写入。理由与 Agent 的白名单完全一致——Direct 地址如果来自控制面发布的 Deployment/Route(P02 已有的可远程 CAS 发布通道),那么"平台运维会话被滥用"或"控制面被攻破"这两个威胁都能直接让 Gateway 去拨任意地址,白名单如果也走同一个通道,就不构成"最后一道防线"。因此设计要求:控制面/Console 可以发布"这条 Deployment 的地址是什么",但 Gateway 在实际拨号前必须再核对一遍**只有 Gateway 本地(启动 flag 或本地文件,类比 Agent 的`-runtimes`配置)才能写入**的允许列表,两者不一致就拒绝派发,不能"控制面说了就信"。
2. **默认值方向相反**：Agent 的`AllowedRuntimes`空列表 = 允许全部(`dispatch.go`注释:"AllowedRuntimes 是本地白名单……为空列表意味着调用方没有收窄"),这个默认是安全的,因为 Agent 本机能运行的 runtime 本身就是运维手动装的,能装什么就是信任边界。Direct 的风险模型不同——"网络可达"不等于"运维批准转发流量",一台内网可达的服务器可能是别的系统,不是这个平台的推理节点。因此 Direct 的地址允许列表**默认值必须是空 = 拒绝全部**,运维必须显式列出host:port或CIDR才能放行,不能沿用 Agent 那种"空即放行"的默认。
3. **允许列表条目的形状**:显式`host:port`或 CIDR,不支持通配符匹配任意路径/端口范围(避免"允许了一个大范围之后靠端口区分安全边界"这种脆弱设计)。基础格式校验(scheme 必须是 http/https、URL 不能带内嵌凭据)可以直接复用`alertengine.validateWebhookURL`同款检查(见`webhook.go:120-131`,已有先例),但这只是格式校验,不能替代第五节的 SSRF 防护——见下节说明两者的关系。

## 四、后端凭据归属

**今天的设计:凭据只在 Agent 本地解出,从不过隧道。** `common/runtime.Config`(`common/runtime/types.go:18-33`)携带`BaseURL`和`APIKey`两个字段,这个结构体只在 Agent 侧被构造(`service/aiServeWeaveAgent/main.go`、`localdiscovery/localdiscovery.go`),Gateway 从不构造它。隧道协议的设计刻意只传引用:`tunnel/client.go:143-149`的`SecretResolver`接口文档写得很直接——"The control plane only ever names a secret [`api_key_ref`], so a compromised Gateway learns no credentials; how a name is resolved (file, environment, external manager)…is entirely this interface's implementation's business",即真正的凭据值只在 Agent 本地由`resolveSecret`(`control.go`)解出,`Config.LogValue`(`common/runtime/config.go:125-128`)的 doc comment 也明确写着"deliberately omits APIKey…so a Config can be logged directly without leaking secrets"。这一整套设计的前提是"Gateway 可能被攻破,凭据必须不经过它"——这正是 AGENTS.md 安全红线"API Key、自定义鉴权头……不得写入日志或错误文本"背后的信任边界设计。

**Direct 结构性打破这个前提。** Direct 没有 Agent 这一环,Gateway 必须自己拿着能拨通后端的凭据发请求——不存在"控制面只传引用,真正的值在别处解出"这个中间步骤,因为"别处"就是 Gateway 自己。这不是一个可以靠加密传输绕过的技术细节,是这个功能本身要求的新信任边界:**采用 Direct 模式,就是显式决定让 Gateway 成为该后端凭据的持有方**,运维在选择"这个节点用 Direct 还是 Tunnel"时,同时也是在选择"这个后端的凭据信任给 Gateway 还是信任给 Agent",这个权衡必须写进 Direct 的运维文档,不能藏在实现细节里。

具体归属规则(设计,非实现):

1. **凭据本身必须来自 Gateway 本地机密来源**(环境变量、挂载文件、外部密钥管理器的引用),**不能**通过控制面发布的 Deployment JSON 里带一个明文/可逆密文字段下发——理由与第三节地址允许列表的"所有权不能落在可远程写的通道"一致:控制面/Console 可以发布"这个 Deployment 该用哪个凭据引用(一个名字)",但引用到凭据值的解析必须发生在 Gateway 本地,类比 Agent 侧`api_key_ref`→`SecretResolver`的模式,只是解析发生的进程从 Agent 换成了 Gateway。
2. **凭据值绝不能出现在`Descriptor`或任何面向控制面/Console 的上报结构里**——对齐`common/runtime/types.go`里`Descriptor`的既有约束("Descriptor must never contain credentials or custom header values"),Direct 目标的能力上报(如果第一节末尾提到的健康探测机制真的做出来)同样不能带凭据。
3. **日志/错误路径复用现有`runtime.Redact`与`Config.LogValue`的脱敏约定**,不允许 Direct 另开一条不脱敏的错误处理路径——这条本质上是"新代码不能豁免既有红线",不是 Direct 专属的新规则,但必须在实现验收里显式检查,因为 Direct 的错误大概率来自`net/http`的调用栈(超时、连接被拒、TLS 握手失败等),这些错误信息里最容易意外携带 URL 查询参数或 Header 值。

## 五、SSRF 防护

### 5.1 现状:全仓库唯一的"外部 URL 校验"先例不足以套用

`alertengine.validateWebhookURL`(`webhook.go:120-131`)是全仓库唯一对一个外部可配置 URL 做校验的代码,逐字引用:

```go
func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("alertengine: webhook URL must use http or https")
	}
	if u.User != nil {
		return errors.New("alertengine: webhook URL must not contain embedded credentials")
	}
	return nil
}
```

它只挡两件事:scheme 必须是 http/https,URL 不能带内嵌凭据(`user:pass@host`)。**它不检查解析出的 IP 是否落在私有网段、loopback、link-local 或云 metadata 地址段**——这不是遗漏,是威胁模型使然:webhook 的目标是运维自己配置的、给自己基础设施用的通知端点(`webhook.go:1-12`的包注释明确写"the downstream here is an operator's own infrastructure, not a tenant-visible authorization path"),不会被调度器反复选中、不会被每个用户请求触发,失败一次记录`NotifyStatus=failed`后直接丢弃(不重试入队)。

Direct 的威胁模型不同:Direct 地址一旦发布,会被`Scheduler.pickBy`反复选中,**每一个匹配该模型的用户请求**都会触发 Gateway 向这个地址发起真实网络连接——如果这个地址能被(a)一次配置错误,或(b)一个被攻破/滥用的平台运维会话,指向内网管理接口、云 metadata 服务(如`169.254.169.254`)、或控制面/Registry/Redis 自己的端口,Direct 就把 Gateway 变成了一台可长期、可重复触发的内网探测器——正是`tunnel/dispatch.go:26-27`那句话描述的风险("turn this Agent into an internal port scanner"),只是主体从 Agent 换成了 Gateway。因此**不能只照抄`validateWebhookURL`当作 Direct 的 SSRF 防护**,它可以作为最低限度的格式校验前置(见第三节末尾),但不能作为唯一防线。

### 5.2 SSRF 防护的判定标准(设计,供未来实现时验收)

1. **必须校验解析后的 IP,不能只看 host 字符串。** 一个 hostname 允许列表本身不足以防 DNS rebinding——攻击者/配置错误可以让允许列表里的域名解析到私有地址。判定标准:允许列表(第三节)本身就应该以"解析出的 IP 段"为最终判据(允许 host 名但校验发生在拨号阶段拿到真实 IP 之后),而不是在允许列表检查之后再走一次独立 DNS 解析——两次解析之间地址可能变化,这是标准的 SSRF 防护反模式(检查时刻与使用时刻不一致,TOCTOU)。
2. **默认拒绝私有/特殊用途地址段**,除非该地址显式出现在允许列表里:loopback(`127.0.0.0/8`、`::1`)、link-local(`169.254.0.0/16`、`fe80::/10`,包含云 metadata 常见地址)、私有网段(`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`)以及未指定地址(`0.0.0.0`)。允许列表本身就是这份防护的"例外声明"渠道——运维如果确实要 Direct 一台内网 GPU 服务器(README.md:133 说的正是这个场景),就必须显式把它的地址写进允许列表,这是刻意的摩擦,不是需要优化掉的不便。
3. **不做通用反向代理。** AGENTS.md 安全红线"Agent 永远不做通用 HTTP 代理:隧道只传运行时语义,不传任意 URL、Host 或 Authorization"对 Tunnel 的约束,对 Direct 同样必须成立——Direct 只能拨 Deployment 声明的固定`BaseURL`加上`common/runtime`适配器已知的固定路径(如 Ollama 的`/api/chat`),不能把客户端请求里的任何字段(Header、Path、Query)转发成决定实际网络目标的依据。这一条决定了 Direct 的实现边界必须复用`common/runtime`现有适配器的请求构造逻辑,不能新写一个"透传"层。
4. **允许列表变更是运维带外操作,不是运行时可调的配置。** 与第三节的"所有权落在 Gateway 本地"呼应——SSRF 防护的强度取决于允许列表改起来有多容易,如果允许列表可以被和地址一样的远程通道悄悄扩大,防护形同虚设。

## 六、验收目标(供未来实现该功能时使用；本项不实现)

Direct 模式若要在未来某个后续任务里交付，至少需要满足：

1. `common/modelroute`（或专用新包）新增地址与连接模式的 schema 字段，经与 P02/P03 相同强度的版本化发布/CAS 校验；`common/modelroute` 现有 `MaxRoutesBytes`/`MaxTargets` 等边界同样适用于新字段，不能引入无界配置。
2. Gateway 新增一个实现 `runtime.InferenceRuntime`/`WorkflowRuntime` 的 Direct 客户端类型（比照 `tunnelserver.NodeRuntime` 的编译期接口断言），且 `Scheduler.pickBy` 或其等价物有一条明确定义的候选发现路径覆盖 Direct 目标（见第一节 1.3 的缺口）。
3. 地址允许列表按第三节实现：默认拒绝、Gateway 本地所有权、按解析后 IP 判定，且有测试覆盖"控制面发布了允许列表之外的地址 → 请求被拒绝而不是被转发"。
4. 后端凭据按第四节归属：有测试覆盖"凭据值不出现在任何 Descriptor/上报结构/日志/错误文本里"，复用 `runtime.Redact`。
5. SSRF 防护按第五节判定标准实现：至少有表驱动测试覆盖 loopback/link-local/私有网段/metadata 地址被默认拒绝、允许列表放行后可通过。
6. 端到端测试证明 Direct 与 Tunnel 对同一个 `runtime.InferenceRuntime` 调用产生一致的响应形状（复用现有 Ollama/vLLM 适配器针对一个真实或伪造 HTTP 后端的测试模式，参考 `e2e/faultinjection_test.go` 已有的 `common/runtime/ollama` 用法）。
7. README/STATUS 的"Direct 待核实"措辞替换为指向具体实现的 PR/commit，不再是设计文档。

## 七、后续实现任务的拆分建议（不在本文档范围内交付）

1. Deployment/Target schema 扩展（依赖 P02 现有 CAS 发布框架）。
2. Gateway 本地 Direct 健康探测循环（复用 `common/runtime` 的 Discover/健康检查逻辑,是 1.3 节缺口的直接后续)。
3. 地址允许列表与 SSRF 防护(第三、五节)。
4. 凭据本地化归属与解析(第四节)。
5. ComfyUI WebSocket 事件的 Gateway 侧代理(表格里单独列出的差异项)。
这五项彼此有依赖顺序(1→2 可并行于 3/4,5 独立),建议拆成独立任务而不是一次性实现,每项各自有可验证的验收标准,不要把"支持 Direct"当成一个不可分割的大任务规划。

## 八、已知缺口清单

1. **候选发现/健康模型完全未设计**(1.3 节)——"两侧同构"目前只在接口调用层成立,候选发现层的方案本文档只给出问题描述,没有给出具体设计(需要独立评估:Gateway 自建探测循环 vs 接受无实时健康的降级模型,是需要权衡资源开销和一致性的架构决策,超出本次核实范围)。
2. **没有真实 SSRF 攻防验证**——第五节的判定标准是基于代码阅读和已知 SSRF 防护通用实践给出的设计要求,没有针对本仓库写一个真实的"Direct 指向内网地址被拒绝"的可运行测试(因为 Direct 代码本身不存在,无法测试一个不存在的功能;这条缺口会随第六节验收目标在未来实现时一并清除)。
3. **ComfyUI Direct 的 WebSocket 转发细节未设计**——第二节表格只指出这是一块全新代码,没有给出具体协议设计,需要在实现该项时单独设计(可能需要参考 Agent 现有的 ComfyUI WS 代理实现作为起点,但部署位置从 Agent 换到 Gateway,信任边界不同,不能直接照搬)。
4. **允许列表与凭据配置的具体文件格式/flag 名未定义**——第三、四节给出的是所有权原则(必须是 Gateway 本地、默认拒绝/无凭据),没有给出具体 flag 名或文件 schema,留给实现阶段与 Agent 现有`-runtimes`配置的具体格式对齐时再定。
5. **A05(调度扩展拆分)与本项有交叉但未合并处理**——A05 提到"受信任授权与 Agent 自报标签分开",如果 Direct 目标未来也参与`NodeSelector`匹配,它的标签来源(运维声明,而非任何一方"自报")需要在 A05 的调度扩展设计中一并考虑,本文档不展开。

## 九、与 STATUS.md 的关系

本文档完成 A04 要求的两个动词——"核实"（第一节，附代码引用坐实"Direct 在 Go 代码里完全不存在，'两侧同构'的声明止步于接口层"）与"定义"（第三至五节，分别定义地址允许列表、后端凭据归属、SSRF 防护的设计原则和判定标准，均对齐 AGENTS.md 既有安全红线的同等强度要求，而不是凭空发明一套新标准）——并给出验收目标（第六节）和实现任务拆分建议（第七节），供未来真正实现 Direct 模式时使用。本项**不实现 Direct 本身**，不新增任何 Go 代码或测试，第八节缺口不阻塞勾选，与 A01 同一先例。STATUS.md 的 A04 行据此勾选为完成并链接回本文档；根 README.md 中"Direct 的可交付范围待 A04 核实"（README.md:13）与 ComfyUI 章节"Direct 待核实"（README.md:245）两处措辞同步更新为指向本文档，但**不改变"Direct 尚未交付"这一事实**——本文档划的是边界和后续实现的路线,不是实现完成的声明。
