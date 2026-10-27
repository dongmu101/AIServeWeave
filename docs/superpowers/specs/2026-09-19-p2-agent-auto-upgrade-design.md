# P2「Agent 自动升级」：现状核实与范围评估

本文档评估根 STATUS.md「P2：后续扩展」第一条（STATUS.md:124）里的一项——「Agent 自动升级、Kubernetes 部署、Registry 高可用」三者中的第一个。上次会话（STATUS.md:124）已核实这三项规模差异很大且互相独立，Agent 自动升级「在代码里完全不存在……只有 `-version` flag 做可观测性，离自更新还差整条分发/签名/灰度/回滚链路」。本文档核实这句话背后的具体代码依据、推导安全边界、给出候选方案的取舍分析，并拆出可独立排期的子任务列表。

范围边界：**本项只做现状核实、威胁模型推导与方案评估，不含新代码或新测试**，与 [Registry 高可用评估](2026-09-18-p2-registry-ha-evaluation.md)、[独立 Tunnel Gateway 与事件基础设施评估](2026-09-18-p2-tunnel-gateway-event-infra-evaluation.md) 同一先例——原因也相同：这是一件此前从未被讨论过、且触及仓库两条核心安全红线（隧道协议「不传 URL/不传命令」、Agent 依赖红线「只有 gRPC/protobuf/coder-websocket」）的全新领域，签名密钥归属、二进制分发的物理承载点这两个问题本身就是需要维护者裁决的架构决策，不应该在一次评估里顺手拍板。不同于「模型分发」「ComfyUI Managed Docker」两份先例文档——那两份在核实与拆分之后，同一 session 内落地了风险最低的子任务一——本文档不落地任何子任务，子任务一本身也留给后续 session。

## 一、现状核实

### 1.1 版本信息今天只做可观测性，没有一行自更新代码

`service/aiServeWeaveAgent/main.go:41` 定义 `var version = "dev"`，其文档注释（第 33-40 行）说明用途：「version 在隧道握手中上报给 Registry，也是 `-version` 打印的内容。构建时通过 `-ldflags="-X main.version=..."` 注入……直接 `go build` 得到的就是 "dev"」。这个字符串被用在三处，全部是只读上报：

- `main.go:71/74-77`：`-version` flag，打印后直接退出。
- `main.go:701`：Hello 握手的 `AgentVersion` 字段（供 Registry/Gateway 记录这是哪个版本的 Agent）。
- `main.go:740`：另一处握手/上报路径的 `AgentVersion`。

`Dockerfile:24/41`：`ARG VERSION=dev`，`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}"`——三个 Go 服务共用同一份构建配方，由 `SERVICE` 选择产物，没有针对 Agent 的特殊构建/签名步骤。全仓库 `grep` `syscall.Exec`、`os.Executable`、`crypto/ed25519` 零命中——自替换重启、二进制自省、签名校验，三者都不存在任何代码雏形。

### 1.2 Agent 的部署故事是裸主机进程，没有假设任何监督器

`deploy/docker-compose.yaml:459-474` 的注释明确写着「Agent 在宿主机上跑，因为它要连本机的推理后端」，给出的启动方式是直接 `go run ./service/aiServeWeaveAgent ...`——不是 compose service，没有 `restart: unless-stopped`（该文件里其余七个 service 都有这一行，唯独 Agent 完全不在 compose 管理范围内）。这意味着：

- **没有 systemd/docker 级别的「进程退出自动拉起」可以假设存在。** 运维如果真的用 systemd 或某种进程管理器包一层 Agent，那是运维自己的选择，不是仓库既有部署故事的一部分——与 Registry 高可用评估文档核实「本机开发环境无 K8s manifest」是同一处境的另一种表现（K8s 部署本身也是 STATUS.md 这一条的未排期子项）。
- 任何依赖「进程退出后被外部拉起」的自更新方案，在这个仓库今天的部署故事下**不能被当作默认可用的机制**，只能是「如果运维已经加了监督器，这条路径可以工作」的附加选项。

### 1.3 隧道协议的两条 load-bearing 规则同样约束这个功能

`api/proto/tunnel/v1/tunnel.proto` 头部注释（模型分发子任务二设计文档第一节、ComfyUI Managed 子任务二设计文档第一节均已引用并遵守）：

> No message here carries a credential.
> No message here can express "fetch this URL" or "run this command". The Agent is not a general-purpose proxy...

AGENTS.md 安全红线原文：「Agent 永远不做通用 HTTP 代理：隧道只传运行时语义，不传任意 URL、Host 或 Authorization。」「`runtime_id` 必须命中 Agent 本地白名单才执行——即使 Gateway 被攻破也不能让 Agent 访问未声明地址。」

一个「自动升级」功能如果字面实现「控制面告诉 Agent 去这个 URL 下载这个二进制并执行」，同时撞上这两条规则的两个方向：「传 URL」和「传命令」——比模型分发子任务二当初设想的「下发 URL」更严重，因为这次下发的不是数据文件，而是**将来会被 `exec` 执行的代码本身**：一个被攻破的 Gateway 副本如果能让 Agent 从任意地址拉取任意二进制并运行，等价于对整个机群的远程代码执行。这条推导比模型分发/ComfyUI Managed 的同类推导更硬，没有讨论空间。

### 1.4 Agent 依赖红线：不能引入 Docker SDK 式的重依赖，但也不能没有签名校验

AGENTS.md：「Agent 与 Registry 的直接依赖只有 gRPC、protobuf 与 `coder/websocket`，这条线要守住。」`hostresources`（shell out 到 `nvidia-smi`/`sysctl`）、`modelpull`（纯标准库 `net/http`+`crypto/sha256`）、`comfyuimanaged`（shell out 到 `docker` CLI）三个包分别代表这条红线下已经验证过的三种实现 idiom：不新增 SDK 依赖，要么用标准库，要么 shell out 到一个固定的外部命令。

自动升级需要的「验证一个二进制文件的真伪」，标准库 `crypto/ed25519`（Go 1.13 起内置）+ `crypto/sha256` 已经足够构造一个「对制品哈希做 Ed25519 签名，公钥打进 Agent 二进制本身」的方案，**不需要新增任何第三方依赖**——这是本文档推荐方案的立论基础之一（第三节 3.1）。但这也意味着**密钥管理本身完全靠 Agent 自己的构造/维护者流程**，没有 KMS/HSM 这类基础设施可以依赖（引入会立刻违反依赖红线，且是全新的运维负担），私钥保管方式需要维护者裁决（见第六节已知缺口）。

### 1.5 「按名字触发，本地已声明」这套模式已有两次成熟先例，可以直接复用

模型分发子任务二（`docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md`）与 ComfyUI Managed 子任务二（`docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-subtask2-design.md`）都在设计过程中撞上 1.3 节的同一条规则，并各自独立收敛到同一个模式：

- 模型分发：控制面只能说「现在开始拉 `qwen3-coder:30b`」，Agent 在自己本地清单（`-model-pull-manifest`）里按名字查找，名字不在清单里就是纯本地拒绝，从不发起网络请求。协议是 `ModelPullTrigger{names}` / `ModelPullReport{pulls}`，走 `Control` 流（心跳/`RuntimeConfig`/`GatewayRoster` 同一条节点级信道），不是 `Serve` 槽（不属于任何 `runtime_id`）。
- ComfyUI Managed：镜像/GPU/挂载路径 100% 来自 Agent 本地 flag 不变，控制面只能下发一个封闭枚举动作（`ComfyUIManagedAction{START/STOP/RESTART}`），不携带容器名（今天只有一个实例）。协议同样走 `Control` 流的 `GatewayControl`/`AgentControl` oneof 新增分支。

两份文档都强调：**成功与失败都通过状态报告观察，不设专门 ack 帧**（`GatewayControl_Config` 的既有先例），且**错误原因是封闭词表，从不透传原始错误文本**（模型分发子任务二第一节：预签名 URL 的签名 token 可能藏在 `*url.Error` 里）。

Agent 自动升级完全符合这个形状：控制面能触发的只应是「检查有没有新版本」「升级到某个版本号」「回滚」，而「版本号对应哪个 URL、哪个签名」永远只在 Agent 本地声明（flag 或本地清单文件），不经隧道下发——与模型分发子任务一的「运维本机可信配置」是同一信任级别，不是「网络可达就默认可信」。

### 1.6 已有的排空（drain）机制可以直接复用，方向需要反过来

`service/aiServeWeaveAgent/tunnel/control.go:463-495` 的 `drain`/`waitInFlight`：Gateway 副本关闭时下发 `GatewayControl_Shutdown`，Agent 侧 `c.setState(StateDraining)` → 广播 `Draining` → `SetDraining(true)` 让槽池停止接受新派发 → `waitInFlight(deadline)` 轮询 `Client.InFlight()`（`pool.go:286-288`，`cfg.InFlight` 或 `pool.InFlight()`）直到归零或超时。这条链路今天**只在「Gateway 主动通知 Agent 它要走了」这个方向上使用**——是 Gateway 一侧要退出，Agent 被动配合排空。

Agent 自动升级需要的是**反方向**：Agent 自己决定要重启（因为要换二进制），需要主动对它自己持有的、连到多个 Gateway 副本（`tunnel.Roster`，上限 16）的**每一个** `Client` 调用 `SetDraining(true)` 并汇总等待所有连接的 `InFlight()` 归零，再执行替换/重启——不是被某一个 Gateway 副本单方面告知。这需要在 `tunnel.Manager` 层新增一个「本地发起、跨全部连接排空」的方法，而不是复用 `control.go` 里绑定在单个 `Client`/单次 Control 会话上的 `drain`；两者共享的是「`SetDraining`+`waitInFlight` 这套原语」，不是同一个调用入口。

### 1.7 Console/控制面聚合已有先例，可供未来灰度复用

模型分发/ComfyUI Managed 子任务二都止步于「Gateway 一侧的触发/查询能力，不做跨副本路由聚合」，把「控制面统一转发、按租户/按机群批量操作」推迟到各自的子任务五。STATUS.md 表格记录 `common/nodeview` 是「机群清单的契约：Gateway 报告已连接节点、控制面聚合后交给运维控制台的形状」——这条现成的聚合链路是未来「按百分比/按标签选择一批节点做灰度」时应该复用的落脚点，而不是重新发明一套机群发现机制。

## 二、威胁模型与边界推导

综合第一节的核实，Agent 自动升级必须满足的边界（不满足任何一条都是安全缺陷，不是风格问题）：

1. **隧道协议里不出现任何「下载地址」或「可执行命令」字段。** 控制面能表达的只是「检查」「升级到某个已声明的版本号」「回滚」三个封闭动作，版本号→二进制来源→签名这条映射永远只活在 Agent 本地。
2. **二进制在执行前必须验证签名，而不仅是校验和。** 模型分发子任务一用 SHA256 校验和防止「传输损坏」，但校验和不能证明「这个字节流是维护者发布的」——一个能诱导 Agent 从某个地址下载的攻击者也能同时伪造校验和。可执行代码的信任级别高于模型文件，必须有一个攻击者无法伪造的签名（第三节 3.1）。
3. **签名公钥打进 Agent 二进制本身（编译期常量），私钥完全脱离 Agent 运行环境。** 与 CA 私钥「must stay confined to a single process」（`internal/ca/ca.go:1-6`，Registry 高可用评估文档已引用）同一精神——签名私钥的暴露面必须比 Agent 进程本身更小，因为它一旦泄漏，攻击半径是整个机群将来会执行的每一个二进制。
4. **不能假设有外部监督器会在 Agent 退出后拉起它**（1.2 节）。默认路径必须是 Agent 自己完成「验证通过 → 排空 → 落地新二进制 → 无缝切换执行」的全过程，不能设计成「Agent 退出，寄望于外部把它重新启动」这种在当前部署故事下不成立的路径。
5. **升级前必须排空正在处理的请求**，复用第 1.6 节已有的 `SetDraining`/`waitInFlight` 原语，方向反过来、跨全部 Gateway 连接聚合等待——与 ComfyUI Managed 文档「升级前检查正在运行的 Job」是同一类「排空升级检查」精神,但落地机制不同（ComfyUI Managed 检查的是节点上的 Job 路由绑定，这里检查的是 Agent 自己隧道连接上的 `InFlight`）。
6. **回滚同样要过第 2/3 条的签名校验**——不能因为「这个二进制之前跑过」就默认信任它，理由与「即使是白名单里的东西，也要在使用前重新校验」的既有精神一致（例如 `runtime_id` 白名单每次派发都校验，不是只在注册时校验一次）。
7. **灰度/分阶段发布不需要新的广播原语。** 每个节点的隧道连接已经是独立寻址的（`TriggerModelPull(nodeID, ...)`/`TriggerComfyUIManagedAction(nodeID, ...)` 的既有形状），分阶段发布是控制面/运维一侧「先对一个子集发触发、观察状态报告、再扩大范围」的编排逻辑，不需要 Agent 或隧道协议理解「灰度」这个概念本身。

## 三、候选方案

### 3.1 二进制来源与真伪校验

| 方案 | 说明 | 取舍 |
| --- | --- | --- |
| (a) 仅校验和（无签名） | 沿用模型分发子任务一 SHA256 的做法 | **拒绝**——校验和只防止传输损坏，不防止「校验和本身也是伪造的」；可执行代码不能只有这一层防护，违反第二节第 2 条 |
| (b) Ed25519 签名清单，标准库实现 | Agent 本地维护一份「版本号 → 下载 URL、SHA256、Ed25519 签名」的清单（结构上直接类比 `modelpull.Spec`），签名对象是「版本号+SHA256」这个元组或整份清单，公钥编译期常量打进 `main.go`（类似 `version` 变量，但是通过 `-ldflags -X` 或 `go:embed` 注入一份公钥材料）；下载沿用 `modelpull` 已验证的 stdlib `net/http`+断点续传实现，落地后用 `crypto/ed25519.Verify` 校验签名，签名不过直接拒绝、不落地、不执行 | **推荐**——零新依赖（`crypto/ed25519`、`crypto/sha256` 均为标准库），复用 `modelpull` 已经测试过的下载/续传/配额逻辑，签名校验是本方案与模型分发的唯一实质差异 |
| (c) 只信任 TLS（HTTPS 分发点本身的证书） | 假设分发点用 HTTPS 且证书受信任即可 | **拒绝**——TLS 只保证「传输链路没被中间人篡改」，不保证「发布这份文件的人是维护者本人」，一旦分发主机（例如某个对象存储 bucket）本身被攻破或配置出错（例如权限放宽），攻击者可以在合法证书下直接替换文件；签名校验的是「内容来自持有私钥的人」，TLS 校验的是「链路没被劫持」，两者不能互相替代 |

**签名对象设计**：清单本身（版本号列表+各自的 SHA256）整体签名一次，而不是每个二进制单独签——这样清单被篡改（比如删掉一个版本、或把某个版本号指向别的哈希）会立刻被发现，而不只是「单个二进制被替换」这一种攻击面，与 `modelroute.Digest(routes)` 给整份路由表算版本哈希、供 CAS 发布使用是类似的「对声明本身做完整性保护」的思路（`common/modelroute/modelroute.go:132-140`），只是这里额外加一层非对称签名而不只是哈希。

**私钥归属未决**：本文档不代为决定私钥保管在哪（维护者本机、CI 密钥库、还是复用 Registry 的 CA 基础设施——但 CA 私钥的用途是给节点证书签名，语义完全不同，不应该被借用来签发布物），这是需要维护者裁决的问题（第六节）。

### 3.2 控制面触发机制

沿用第一节 1.5 节的既有模式，新增 `Control` 流上的一对消息（草图，具体字段留给子任务一落地时定稿）：

```
message AgentUpgradeAction {
  AgentUpgradeActionType action = 1; // CHECK / UPGRADE / ROLLBACK
  string target_version = 2;         // 必须命中 Agent 本地清单里的某个版本号；UPGRADE/ROLLBACK 必填，CHECK 可留空
}

message AgentUpgradeReport {
  string current_version = 1;
  AgentUpgradeState state = 2;       // IDLE / CHECKING / DOWNLOADING / VERIFYING / DRAINING / RESTARTING / FAILED
  AgentUpgradeFailureReason reason = 3; // 封闭词表，不携带原始错误文本
  int64 updated_unix_ms = 4;
}
```

`target_version` 是一个版本号字符串，不是 URL；Agent 收到后在本地清单里查找，找不到直接以 `FAILED`/`UNKNOWN_VERSION` 报告，从不发起任何网络请求——与 `ModelPullTrigger`「名字不在清单里就是纯本地拒绝」完全同构。CHECK 动作不下载不重启，只把 Agent 本地清单与当前 `version` 比较，报告是否有更新的已知版本（这本身就是子任务一可以独立交付的最小范围，见五）。

### 3.3 自重启机制

| 方案 | 机制 | 适用前提 | 取舍 |
| --- | --- | --- | --- |
| (a) `syscall.Exec` 自替换进程 | 新二进制验证通过后，当前进程直接 `syscall.Exec(newBinaryPath, os.Args, os.Environ())`，同一 PID 换掉自己的镜像，不产生新进程、不依赖任何外部机制 | 仅 Unix（Linux/macOS 均支持，Windows 没有等价系统调用）；与 1.2 节「不能假设有监督器」的部署故事完全匹配 | **推荐为默认路径**——这是唯一不依赖任何外部假设、在「裸主机 `go run`/编译产物」这种今天唯一被验证过的部署方式下就能工作的机制 |
| (b) 退出特定码，依赖外部监督器重启 | Agent 验证并落地新二进制后以约定退出码退出（例如区分「正常退出」与「请把我重新拉起来」），由 systemd `Restart=on-failure`/Docker `restart` 策略拉起新进程 | 要求运维已经用监督器包装了 Agent 进程 | 1.2 节已核实这个前提在仓库既有部署故事里不成立，只能作为**可选的兼容路径**——如果运维确实用了监督器，也应该允许这条路径工作，但不能是唯一路径 |
| (c) Agent 自己 fork 一个新版本子进程，旧进程退出前把隧道连接"移交"给新进程 | 类似部分反向代理/负载均衡器做"零停机重载"的手法（fork + 继承监听 fd） | Agent 不监听任何端口（AGENTS.md 安全红线：「Agent 只主动出站建连，从不监听公网端口」），没有需要移交的监听 fd；隧道连接是 Agent 主动拨出的 gRPC 客户端连接，新进程重新拨号即可，不需要"继承"旧连接 | **拒绝，且没有必要**——这个技巧是为"不能中断正在监听的端口"设计的，Agent 恰好没有这个问题（它是纯客户端），引入 fork+fd 继承只会平白增加复杂度 |

**推荐**：(a) 为默认且唯一必须实现的路径，(b) 作为附加的、面向"运维已自建监督器"场景的可选退出码约定（不新增复杂度：无非是在 (a) 失败或被显式配置关闭时退出而不是自替换），(c) 不采用。

`syscall.Exec` 执行前必须完成：验证签名（3.1）→ 排空当前进程持有的全部隧道连接（第二节第 5 条，复用 `SetDraining`/`waitInFlight`，跨 `tunnel.Manager` 持有的每个 `Client` 聚合等待，超时则记录日志并强制继续——升级不应该因为一个卡住的长请求无限期悬挂）→ 把新二进制原子改名到位（模型分发子任务一已验证的 `.part` 临时文件+原子 rename 模式直接复用）→ `syscall.Exec`。整个过程 Agent 对 Gateway 呈现为「先短暂排空、连接短暂断开、新版本重新握手」，与 Agent 进程本来就可能因为任何原因重启（例如运维手动重启）时的行为一致，不需要 Gateway/隧道协议新增任何"这是一次升级导致的重连"的特殊语义。

### 3.4 分阶段发布（灰度）

不需要新的隧道原语（第二节第 7 条）。控制面侧的编排选项：

| 方案 | 说明 |
| --- | --- |
| (a) 运维手动指定 `node_id` 列表分批触发 | 复用 `TriggerModelPull(nodeID, ...)` 同款「按节点 ID 触发」HTTP 端点形状（比照 `-model-pull-addr`/`-comfyui-managed-addr`），运维自己决定第一批打几个节点、观察 `AgentUpgradeReport` 后再扩大 |
| (b) 控制面按标签/百分比自动选择灰度分组 | 需要先有 `common/nodeview` 聚合出的机群清单可供按标签筛选，属于子任务五范围；本文档不推荐在 v1 做，见第五节 |

**推荐** (a)：v1 只需要「能对单个/一批已知 `node_id` 触发」这个原语，编排逻辑（先打 5%、观察、再打剩余）留给运维手动操作或未来的控制面自动化,不需要 Agent/Gateway 侧新增任何"这是第几批"的状态。

### 3.5 回滚

- **保留 N 份历史二进制**（建议 N=2：当前 + 上一个版本），命名为 `agent-<version>`，用一个符号链接 `agent-current -> agent-<version>` 指向实际执行的文件，`syscall.Exec` 目标固定是符号链接路径，切换版本只是原子地改写符号链接指向再 `Exec` 一次。
- **回滚同样要走完整的签名校验流程**（第二节第 6 条）——回滚目标如果已经在本地磁盘上，仍要重新校验它的签名（防止磁盘上的旧文件在两次运行之间被篡改；这个假设成本很低，因为验证只是一次 Ed25519 Verify，不值得为了省这一步而引入"曾经验证过就不用再验证"的例外）。
- **磁盘上的旧版本本身也需要来自第 3.1 节的清单**——回滚不是"把上一个 `.part` 文件捡回来"，而是清单里某个版本号对应的、已经验证过的产物，与"升级到某版本"是同一条代码路径，只是 `target_version` 指向的是一个更旧的版本号。

## 四、明确排除范围（v1 不做）

- **不做基于 cron/时间表的自动升级。** v1 只支持手动/控制面显式触发的 CHECK/UPGRADE/ROLLBACK，不做"发现新版本自动升级"的无人值守路径——自动执行会把"验证签名+排空+重启"这条链路的任何一处缺陷放大成机群范围内自动触发的故障,在没有真实故障注入验证之前不应该默认开启。
- **不做健康检查失败后的自动回滚。** UPGRADE 失败（签名不过、下载失败、`syscall.Exec` 本身失败）由 `AgentUpgradeReport` 报告 `FAILED`，回滚仍是一次显式的 ROLLBACK 触发,不设计"升级后自动探测健康状态、不健康就自动切回旧版本"的逻辑——这本身是一个复杂的功能（需要定义"健康"、需要给探测窗口设时限），且当前没有生产负载验证过 Agent 重启后多快能被判定为"健康"，与 A01 已核实的"从未跑过真实生产负载"是同一处境。
- **不做二进制差分/增量补丁（delta patching）。** 只做整份二进制替换，与模型分发/ComfyUI Managed 现有子任务一律"不做增量、只做整份替换/整份下发"是一致的简化方向。
- **不新增跨平台构建矩阵。** 只支持 Agent 今天实际会被构建/运行的目标（Dockerfile 单一 `SERVICE` 选择、无 GOOS/GOARCH 矩阵；`syscall.Exec` 方案本身也把 Windows 排除在外，若未来需要支持 Windows 上的 Agent，需要为该平台单独设计基于方案 3.3(b) 的路径，不在本文档范围内）。
- **不做控制面自动灰度分组算法。** 3.4 节已说明，v1 只做"按 `node_id` 列表手动触发"。
- **不做 ControlPlane/Console 接入。** 与模型分发/ComfyUI Managed 子任务二同一先例——先交付 Gateway 一侧的触发/查询能力，控制面转发层与 Console 可见性是独立的后续子任务。

## 五、子任务拆分建议（均未排期，供未来 session 参考）

1. **子任务一**：契约与「仅检查」路径——`common/agentupgradestatus` 共享类型包（State/Reason 枚举，比照 `common/modelpullstatus`）；`tunnel.proto` 新增 `AgentUpgradeAction`/`AgentUpgradeReport`（`Control` 流 oneof 分支，比照 `ModelPullTrigger`/`ComfyUIManagedAction`）；`common/tunnelwire` 编解码；Agent 侧新增 `-agent-upgrade-manifest` flag 加载本地清单（结构类比 `modelpull.Spec`，新增字段 `Signature`）与公钥常量；只实现 CHECK 动作（比较清单与当前 `version`，报告是否有更新），UPGRADE/ROLLBACK 收到时报告 `FAILED`/`NOT_IMPLEMENTED`。这是风险最低、不涉及任何"替换自己在跑的二进制"的一步，可独立验收。
2. **子任务二**：下载 + 签名校验 + 排空 + `syscall.Exec` 自替换，实现 UPGRADE 动作全链路。复用 `modelpull` 的下载/续传/原子改名实现（新文件或扩展 `modelpull` 本身，需要先决定是否值得为二进制单独开一个包而不是扩展 `modelpull`——留给实现时判断）；`tunnel.Manager` 新增跨全部 `Client` 的排空聚合方法（1.6 节）。这是本项目全新领域里风险最高的一步，需要重点覆盖"排空超时后是否强制继续""`syscall.Exec` 失败后如何回退"两类边界测试。
3. **子任务三**：ROLLBACK 动作 + 保留 N 份历史二进制 + 符号链接管理（3.5 节）。依赖子任务二已有的下载/校验/自替换基础设施。
4. **子任务四**：Gateway 侧触发/查询 HTTP 面——新增 `-agent-upgrade-addr` 独立监听器与独立 token（比照 `-model-pull-addr`/`-comfyui-managed-addr` 的"每种写能力一把独立密钥"先例），支持按 `node_id` 批量触发（3.4 节方案 (a)）。依赖子任务一/二先有 Control 流协议。
5. **子任务五**：ControlPlane 跨副本路由聚合 + Console 灰度进度/版本分布可见性。依赖子任务四；可复用 `common/nodeview` 已有的机群清单聚合模式。

依赖关系：子任务一完全自洽,可独立排期验收;子任务二依赖子任务一的协议与清单结构；子任务三依赖子任务二的执行基础设施；子任务四依赖子任务一/二把动作定义清楚；子任务五依赖子任务四。与模型分发/ComfyUI Managed 两份先例文档相同的排期思路：先做协议+最小可验证的一步（这里是"仅检查"），再做真正有风险的执行路径，最后补控制面/Console 可见性。

## 六、已知缺口与需要维护者裁决的问题

- **签名私钥保管方案未定。** 3.1 节已指出私钥不应该复用 Registry CA 基础设施（语义不同），但没有给出具体保管方案（维护者本机离线保管、CI 密钥库、还是其他）——这条决策的影响面是"将来每一个 Agent 会执行的二进制的真伪判定"，理应由维护者裁决，本文档不代为决定，与 Registry 高可用评估文档"CA 私钥单进程持有 vs 高可用"的裁决同一性质。
- **二进制分发的物理承载点未定。** 模型分发/ComfyUI Managed 的制品来源（模型文件、Docker 镜像）已经有成熟的外部生态（对象存储、镜像仓库）可以直接引用；Agent 二进制是这个项目自己的构建产物，"发布后放在哪里让 Agent 用标准库 `net/http` 就能下载到"（GitHub Releases？自建静态文件服务？）目前没有答案，需要维护者结合现有 CI/CD 流程（`scripts/build-release.sh`，本文档未展开核实其细节）决定。
- **是否允许 3.3(b) 的退出码兼容路径默认开启，还是必须显式配置。** 本文档倾向"默认只用 (a) 自替换，(b) 是需要显式打开的选项"，但没有强定论，留给子任务二实现时结合运维反馈决定。
- **排空超时后的行为需要一个具体数值和"强制继续 vs 放弃升级"的默认策略**——本文档在 3.3 节只给出方向（超时则记录日志并强制继续），没有给出具体超时时长的建议值，因为项目至今没有真实生产负载下的请求时长分布数据（与 A01 已核实"从未跑过真实生产负载"同一处境），需要在子任务二实现时先给一个保守默认值,后续按真实观测调整。
- **CHECK 动作报告"有更新"之后，运维如何得知**——子任务一范围内只有隧道层的 `AgentUpgradeReport`，在子任务四（Gateway HTTP 面）落地之前，这个信息只在 Agent/Gateway 自己的日志或 `Snapshot`-类调用里可见,不构成一个完整的"运维可以订阅到的通知"。

## 七、与 STATUS.md 的关系

本文档完成"Agent 自动升级"这一子项的核实与评估：确认它在代码里完全不存在（1.1 节）、部署故事不支持假设外部监督器存在（1.2 节）、隧道协议与 Agent 依赖两条红线对它的具体约束（1.3/1.4 节）、可以直接复用的三处既有先例（1.5/1.6/1.7 节）、二进制来源校验/触发机制/自重启/灰度/回滚五个维度的候选方案与推荐取舍（第三节）、明确排除的 v1 范围（第四节）、五个子任务的拆分与依赖顺序（第五节）、以及两个需要维护者裁决才能继续的开放问题（第六节）。**本项不实现任何代码，不新增测试。** 也未修改 STATUS.md 本身——是否在该条目补充"Agent 自动升级已评估并链接回本文档"这句话，留给下一次真正推进实现（或至少决定采纳本文档结论）的 session 去做，避免本文档单方面代为更新 STATUS.md 的措辞。与 Registry 高可用评估、独立 Tunnel Gateway 与事件基础设施评估同一先例：现状核实、威胁模型推导与方案评估在同一份文档完成，不落地任何子任务。
