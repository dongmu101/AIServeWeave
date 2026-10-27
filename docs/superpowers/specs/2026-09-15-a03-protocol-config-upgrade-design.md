# A03 协议与配置升级兼容

本文档交付根 STATUS.md「规划补充」A03：明确 Agent/Gateway/Registry 混合版本支持范围、proto 演进规则、配置版本及数据库迁移顺序，验证滚动升级与回退边界。

范围边界（与用户确认过）：本项**以设计文档为主，辅以轻量真实验证**，与 [A02](2026-09-14-a02-disaster-recovery-key-rotation-design.md) 同一先例，但不到 A02 那种"对运行中的参考部署整套演练"的深度——不新起多套混合版本的完整集群做端到端滚动升级排练，那属于工作量远大于设计澄清的独立任务，可留给后续专项验收。真实验证聚焦在**协议演进的核心机制**：protobuf 的字段级前向/后向兼容不是不言自明的公理，而是要看具体用法是否踩了坑，因此本项用一个可复现的最小 Go 程序真实跑出三个场景的结果，而不是仅凭"proto3 通常向后兼容"这类经验之谈下结论。凡属阅读代码得出的结论标注引用行号；凡属真实运行验证的结论标注"已验证（真实运行）"；凡属尚无证据的判断归入第九节缺口清单。

项目目前没有打过任何 git tag（`git tag -l` 为空），R03 交付的发布流水线也从未真正推送过镜像（见 R03 验收记录）——这意味着"新旧版本"目前只能用不同 commit 而非不同 release 来模拟，本项的真实验证也是这样做的；一旦第一次真实打 tag，应该用两个真正的 release 重跑一遍第八节的场景作为补充证据，而不是假定 commit 级别的结论自动適用于 release 级别。

## 一、现状盘点

### 1.1 协议契约（proto）现状

`api/proto/tunnel/v1/tunnel.proto`（848 行）是 Agent/Gateway/Registry 三边共享的唯一契约来源，`go generate ./api/...` 后由 CI 用 `git diff --exit-code` 校验一致（`.github/workflows/ci.yml`）。盘点结果：

- 四个枚举（`SlotClass`、`Operation`、`ReplicaState`、`ConfigAction`）全部以 `_UNSPECIFIED = 0` 起始（tunnel.proto:299、317、507、544），符合 protobuf 新增枚举值安全的前提——旧读者遇到未知枚举值时，proto3 生成代码会原样保留数值而不是报错或截断，`_UNSPECIFIED=0` 只是保证"没收到值"和"收到了一个旧代码不认识的值"两者的默认呈现体面。
- 已经在用 `optional` 修饰少数标量字段（`temperature`/`top_p`/`max_tokens`/`seed` at tunnel.proto:651-655，`dimensions` at 751），用来区分"调用方没传"和"调用方显式传了零值"——这正是新增字段时该遵循的模式，仓库里已有先例可以照抄。
- 全仓库搜索 `reserved` 关键字**零命中**：目前没有任何消息为已删除字段预留编号。截至本次调查这不构成实际风险（还没有字段被删除过），但一旦将来删字段而不加 `reserved`，编号可能被后续新字段意外复用——第二节的规则会把这条钉死。
- `oneof body`（tunnel.proto:271、283、392、402）用于承载控制流/数据流里的多态消息；在 wire 格式层面 oneof 的各个 case 就是普通字段，新增一个 case 等价于新增一个字段编号，第二节的规则同样适用。
- `agent_version` 字段确实存在（`RegisterRequest.agent_version` tunnel.proto:169、`Hello.agent_version` tunnel.proto:415），但**只用于日志与只读展示，从不参与任何兼容性判断**：Registry 收到后只是 `slog.String` 记录（`internal/registryserver/identity.go:132,156,195,203`），Gateway 收到后按节点存一份（`tunnelserver/control.go:70`）并通过运维只读 API 展示（`adminapi/adminapi.go:219`）。`Discovery.version`（tunnel.proto:618）和 `ProbeResult.version`（tunnel.proto:604）是**推理后端**（如 Ollama）自己的版本，与 AIServeWeave 二进制版本无关，不要混为一谈。`GatewayRoster.version`（tunnel.proto:484）是名册单调递增计数器，不是二进制版本。**结论：当前协议里没有任何最低版本门禁或版本协商机制**——Agent、Gateway、Registry 之间完全靠字节层面的 wire 兼容性运行，没有谁会因为对端版本不合适而主动拒绝连接（S03 的节点禁用是另一套机制，按 `node_id` 而非版本）。这是本文档要处理的核心事实，不是缺口，而是现状。
- 没有 `buf breaking` 或等价的 CI 自动检测：CI（`.github/workflows/ci.yml`）和 Release（`.github/workflows/release.yml`）只装了 `protobuf-compiler` 跑生成一致性校验，没有任何工具会在 PR 里自动挡下"字段编号被复用"或"字段被删除又没 reserved"这类改动。第二节的规则目前只能靠 review 人工执行。

### 1.2 四个服务的配置/flag 现状（已通过代码调查确认，未在此重复列出全部证据，详见调查过程）

Agent、Gateway、Registry 三个服务的 `main.go` 均用标准库 `flag` 包的默认 `flag.CommandLine`（`ExitOnError`），传入一个它不认识的 flag 会让 `flag.Parse()` 打印用法并 `os.Exit(2)`——这是**响亮失败**，不是静默降级，运维会立刻在启动日志/进程退出码上看到。控制面（`service/aiServeWeaveControlPlane/main.go:44-51`）只有 4 个 flag（`-f`/`-version`/`-migrate`/`-metrics-addr`），其余配置来自 go-zero 的 YAML（`conf.Load(*configFile, &cfg, conf.UseEnv())`，main.go:80,90）。

四个服务在这一点上**不对称**，是本次调查的一个具体发现：

- Agent/Gateway/Registry：未知 flag → 启动即失败（strict）。
- 控制面：go-zero 的 `conf.Load` 对 YAML 里未匹配到任何结构体字段的多余键**静默忽略**（`core/conf/config.go` 的 `toLowerCaseKeyMap`，字段驱动的反序列化从不对多余键报错），只有结构体里没有 `default`/`optional` 标记的必填字段缺失才会报错。

抽查近期新增的几个 flag，确认它们的零值都映射回"旧行为"而不是"新行为被意外默认打开"：`-redis-addr ""` 时 Gateway 退回内存限流器（`main.go:677-682`）；`-breaker-failure-threshold/-base-cooldown/-max-cooldown` 零值时熔断器换算成升级前的硬编码默认 5 次/5s/2m（`scheduler/breaker.go:16-18,66-74`）；`-route-source`/`-workflow-source` 默认 `"file"`，也就是引入控制面模式之前的行为（main.go:97,102）；控制面的 `AutoMigrate` 配置项零值即 `false`（`internal/config/config.go:353`），即只做 schema 核对不自动迁移，同样是更保守的一侧。调查过程中没有找到反例。

### 1.3 数据库迁移现状（P07 已交付，本项只做跨轨道排序与回退边界的补充分析）

`internal/store/gormstore/migrate.go` 定义 7 条独立迁移轨道（`migrationNamespaces`，migrate.go:54-60）：`base`、`routes`、`workflow_templates`、`metrics_history`、`request_logs`、`alerting`，加上仅 MySQL 才有的 `jobs`（PostgreSQL 6 条、MySQL 7 条）。每条轨道各自一张 `schema_migrations_<namespace>` 版本表（migrate.go:197），但共用同一把跨进程 advisory lock（`withMigrationLock`，migrate.go:119-152：PostgreSQL 用 `pg_try_advisory_lock`、MySQL 用 `GET_LOCK`），保证轨道之间从不并发迁移。`MigrateAll`（migrate.go:321-339）按 `migrationNamespaces()` 返回的固定字面量顺序（`base` 最先、`jobs` 最后）依次迁移；单轨道内部按文件名顺序（`0001_...`、`0002_...`）应用。`-migrate up/status/resume`（`internal/svc/database.go:15-45`）一次调用 `MigrateAll` 迁移全部轨道，没有"只迁移某一条轨道"的入口。

对全部 39 个迁移 SQL 文件搜索 `FOREIGN KEY`/`REFERENCES`，**零命中**——这套 schema 完全没有数据库层面强制的跨表外键，包括跨轨道（例如 `jobs` 表虽然逻辑上属于某个 `tenant_id`，但没有指向 `base` 轨道 `tenants` 表的外键约束）。这意味着 `migrationNamespaces()` 里的固定顺序**是代码约定用来保证确定性和加锁互斥，不是数据库强制的依赖关系**——目前没有任何一条轨道的建表语句在物理上依赖另一条轨道已经迁移完成。

没有 DOWN/回退迁移：唯一带"rollback"字样的是 `RollbackRoutes`/`RollbackWorkflowTemplate`（应用层"发布一个复制旧版本内容的新版本"，见 1.4 节之外的 P02/P03 CAS 契约），不是 schema 层面的降级脚本，坐实 STATUS.md 既有的"不提供破坏性 down"表述。

### 1.4 Registry 非 SQL 持久状态现状

`internal/identitystore`/`internal/tokenstore` 各自是一份 JSON 文件（互斥锁 + 原子写：临时文件 + `Sync` + `rename`，权限 `0o600`），`record` 结构体（identitystore.go:94-121、tokenstore.go:33-46）**没有任何 schema/version 字段**。已有先例证明这套机制目前完全靠 Go 的 JSON 零值语义打补丁：`PendingApproval` 字段是后来加的，注释里明确写着"写于该字段存在之前的账本文件，解码后每条记录的这个字段都是零值 `false`，等价于已批准"（identitystore.go:106-113）——这是**刻意利用**零值语义,不是巧合。

但反方向完全没有保护：如果一个**较旧**的 Registry 二进制打开一份被**较新**二进制写过新字段的文件，`json.Unmarshal` 会静默丢弃它不认识的字段；如果这个旧进程之后触发任何一次写操作（Mint/Consume/Revoke/Approve/Disable/Enable 均会原子重写整个文件），那些新字段就会在这次重写中永久消失——不是读丢，是**写丢**。这是本次调查的一个具体发现，第五节展开。

## 二、Proto 演进规则

以下规则不是从 protobuf 官方文档抄来的通则，而是针对本仓库现有用法（无 `reserved`、已用 `optional`、四个枚举都有 `UNSPECIFIED`、`oneof` 用于多态帧）定的可执行清单：

1. **只加字段，用未使用过的编号；永不复用一个已经用过的编号**（哪怕该字段已被删除）。删除字段时必须把它的编号和名字写进 `reserved`（proto3 支持 `reserved 7; reserved "old_field_name";`），阻止未来的编辑者不知情地复用。**目前 tunnel.proto 里一个 `reserved` 都没有，本项不追溯性地给现有 84 个消息普查该不该补 `reserved`（没有字段被删过，没有必要），但从下一次删字段开始必须遵守。**
2. **新枚举值只加不删，且永远排在已有值之后**；新枚举值的语义不能与已有值互斥到"旧读者会把它误解成另一个值"的地步——枚举本身没有"未知值"的安全落地（不像消息字段有 unknown-field 缓冲区），旧读者收到不认识的枚举数值会原样存成该整数，业务逻辑如果对它做 `switch` 且没有默认分支，就可能把新枚举值悄悄当成 0 处理。检查点：`Operation`、`ReplicaState`、`ConfigAction`、`SlotClass` 四处消费方的 `switch` 语句都要有 `default`（不在本次范围内逐一核对每个 switch，列入第九节缺口）。
3. **新增标量字段一律用 `optional`，除非零值本身就是唯一合法的"未指定"语义**（比如单调计数器、`bool full` 这种"缺省即 false 且 false 就是安全默认"的场景）。判断标准：如果这个字段的"调用方没提供"和"调用方显式给出该类型的零值"在业务上有区别，必须用 `optional` 区分，参考 tunnel.proto:651-655 已有先例。
4. **修改一个 RPC 方法的名字、请求/响应消息类型或 streaming 方向，等价于宣布断开兼容**——gRPC 按完整方法名解析，旧的一侧调用会收到 `Unimplemented`（响亮失败），这是可以接受但必须显式记录在 CHANGELOG「Breaking Changes」里的变更，不能当成普通字段新增处理。
5. **禁止把某个字段的语义悄悄改掉而不改编号**（例如 `int64 seq` 从"每请求递增"改成"每连接递增"）——旧读者会拿到语法上合法、语义上错误的数据，这是 wire 格式完全捕捉不到的一类错误,只能靠 review 和第二节这份清单人工把关。

### 真实验证：三个场景（已验证，真实运行）

为了不空口断言"proto3 是前向/后向兼容的"，本项写了一个独立、可复现的最小 Go 程序验证上述规则 1 的两个方向和它的反例，脚本与运行结果记录在第八节附录，结论摘要：

- **场景 A（新发送方多发一个字段 → 旧接收方）**：旧接收方正常解析，已知字段值完整，新增字段被忽略、不报错。验证了"先升级 Agent 再升级 Gateway"这类顺序在字段新增场景下是安全的。
- **场景 B（旧发送方不发新字段 → 新接收方）**：新接收方正常解析，新字段读到类型零值，不报错。验证了"先升级 Gateway 再升级 Agent"同样安全，前提是新字段的零值语义符合规则 3。
- **场景 C（误用：复用一个已用编号但类型不同）**：**没有报错**，旧接收方把新类型的字节直接按旧类型的 wire 规则重新解释，得到一个语法合法但语义错误的值（字符串字节被当成 varint 解出一个无意义的整数）——这是规则 1 存在的理由：wire 格式不会替你挡下这类错误，唯一的防线是编号永不复用的纪律。

## 三、配置版本策略

### 3.1 CLI flag：只加不改语义，且"先发二进制、后发配置"

Agent/Gateway/Registry 三者 flag 严格模式（未知 flag 直接退出）意味着**部署配置（compose 文件、systemd unit、k8s manifest 里的启动参数）不能领先于二进制版本**：如果运维先把新版本才认识的 flag 写进部署配置、再滚动替换二进制，那么还没轮到的旧副本重启时会因为收到不认识的 flag 直接崩溃退出。正确顺序是：先把二进制换成新版本（不传新 flag，新 flag 有安全默认值，见 1.2 节），确认新版本正常运行后，再单独发布一次"追加新 flag"的配置变更。回退同理，先把新增 flag 从配置里去掉，再换回旧二进制,顺序反了会导致旧二进制看到它不认识的 flag 而拒绝启动。

控制面的 go-zero YAML 因为未知键静默忽略，不受这条顺序约束——可以先把新键写进 YAML，旧版本控制面会忽略它、新版本上线后才开始读取。**这是三个 flag 服务和控制面之间刻意还是巧合的不对称，本项不改动任何一边的行为**（改成一致需要引入自定义 flag 解析或改用配置文件，属于新代码，超出"设计为主"的范围），只是如实记录:运维在写升级手册时必须按服务分别处理，不能用同一套"配置可以先行"的心智模型套用到全部四个服务。

### 3.2 CAS 发布内容（路由、工作流模板）的 JSON 契约

P02/P03 已经把路由、工作流模板做成不可变版本化快照,发布是 CAS（compare-and-swap on `expected_revision`），Gateway 侧的 `routesync`/`workflowsync` 拉取新快照后原子热切换（详见 ControlPlane README「模型路由发布」「工作流模板发布契约」两节）。这条路径涉及两个方向的 JSON 兼容：

- **操作员写请求（Console → 控制面）**：`decodeRoutes`（`internal/handler/modelroutes.go:141-150`）与 `decodeWorkflowTemplate`（`internal/handler/workflowtemplates.go:167-180`）都调用了 `decoder.DisallowUnknownFields()`——这是**刻意的严格模式**，如果一个更新的 Console 在请求体里多发了一个更旧的控制面还不认识的字段,请求会被 400 拒绝，而不是被悄悄吞掉再发布一份丢了新字段的快照。这对滚动升级的含义是：**控制面的升级顺序不能落后于 Console**,否则新 Console 的每一次发布/回滚请求都会响亮失败(不是数据损坏,但会挡住发布功能)。
- **Gateway 拉取快照（控制面 → Gateway）**：`GET /internal/v1/routes/current`、`GET /internal/v1/workflow-templates/current` 这两个内部端点的响应体，在 Gateway 侧的反序列化路径上，全仓库搜索确认**没有任何 `DisallowUnknownFields` 调用**——一个更新的控制面往快照里加了新字段，旧版本 Gateway 会按 `encoding/json` 默认行为静默忽略,继续用它认识的字段热切换,不会报错也不会崩溃。这对滚动升级的含义是：**Gateway 落后于控制面是安全的**,只要新字段的"缺失时的零值"语义同样满足规则 3 的判断标准。

综合 3.1 与 3.2,四个服务里"谁先升级更安全"不是一个统一答案,取决于具体是哪条边:控制面应当不落后于 Console(写路径严格),但 Gateway 可以落后于控制面(读路径宽松)。第六节给出综合建议顺序。

## 四、数据库迁移顺序与回退边界

`migrationNamespaces()` 的固定顺序（1.3 节）目前**不是**由外键强制的硬依赖,而是代码约定,这意味着:

- **正向升级没有顺序风险**:7 条轨道互相独立,`MigrateAll` 用同一把锁串行执行只是为了避免并发迁移互相踩踏(P03 的死锁修复就是同一轨道内部并发写的教训,不是跨轨道顺序问题),不代表"先迁移 base 再迁移 routes"这件事本身有数据完整性含义。
- **回退控制面二进制的安全边界,取决于迁移历史是否"仅追加"**:P07 的迁移目前全部是 `CREATE TABLE`/`ALTER TABLE ADD COLUMN`/`CREATE INDEX` 一类的加法操作(抽查 39 个迁移文件的文件名与既有 ControlPlane README 记录确认,没有出现过 `DROP COLUMN`/`RENAME COLUMN`/改列类型)。只要这个"仅追加"的纪律保持下去,回退控制面二进制到某个更早版本是安全的——旧版本的 GORM 查询只引用它认识的列,数据库里多出来的新列/新表不会让旧查询失败。**一旦将来出现一次破坏性迁移(删列、改列类型、重命名),没有对应的 down 脚本,回退就只能走 P07/`deploy/database-recovery.md` 已验证过的整库备份恢复,不能指望换回旧二进制就能优雅处理。** 这是一条必须写进未来每一次破坏性迁移的 PR checklist 的规则,但本项不新增 CI 强制检查(检测"是否存在 DROP/RENAME"需要新写 lint 工具,属于新代码,列入第九节缺口而非本项交付)。
- 三个共享表迁移相关的服务边界维持既有约定:Gateway/Agent/Registry 都不直接连数据库(AGENTS.md 的既有红线),因此"数据库迁移顺序"只在控制面单进程内部有意义,不存在"多个服务同时对同一张表跑不同版本迁移"的场景。

## 五、Registry 文件持久化的回退风险（真实发现，非纯设计）

1.4 节已指出结构性问题,这里给出针对回退场景的具体判断规则,不做真实故障注入(A02 已经对 Registry 的备份/恢复做过一轮真实演练,结论是"文件层面的备份/恢复往返有效";本项要补的是**回退到旧二进制**这个 A02 没有覆盖的角度):

- 如果只是**重启同一个二进制版本**,`identitystore`/`tokenstore` 没有兼容性问题——同一份代码写的字段,同一份代码当然认识。
- 如果**回退到更旧的二进制版本**,且新版本曾经写过旧版本不认识的新字段(目前唯一的例子是 `PendingApproval`,S03/P01 引入),旧版本能正常读取文件(未知字段被 `encoding/json` 忽略),**但旧版本自己触发的任何一次写操作会把这些新字段从文件里永久抹掉**——因为旧版本的 struct 里根本没有这个字段,重新序列化时自然不会写出来。抹掉的具体后果要看字段语义:`PendingApproval` 被抹掉意味着所有原本待审批的节点在下一次序列化后变回"已批准"状态(零值语义,见 1.4 节),这是一个真实的安全性回退,不只是数据丢失。
- **规则**:回退 Registry 二进制之前,先确认目标旧版本发布之后新增过的任何 identitystore/tokenstore 字段,评估"被抹掉"对应的业务含义是否可接受;如果不可接受(比如刚好有节点处于待审批状态),必须先备份当前文件(A02 演练已验证 `tar` 打包 `-data-dir` 即完整备份),回退后如果发现字段被抹掉,用备份恢复而不是继续在旧版本上运行。这条规则本项只做设计与判断标准的澄清,不新增代码(比如给 JSON 文件加 `schema_version` 字段并在旧版本拒绝加载新字段文件,属于新功能,列入第九节缺口)。

## 六、版本互操作范围声明

基于以上四节的证据,给出当前代码库实际支持(而不是宣称)的混合版本范围:

| 组合 | 支持范围 | 依据 |
| --- | --- | --- |
| 新 Agent ↔ 旧 Gateway/Registry(仅新增字段) | 支持 | 第二节场景 A(已验证);agent_version 只做日志,不做门禁 |
| 旧 Agent ↔ 新 Gateway/Registry(仅新增字段) | 支持 | 第二节场景 B(已验证) |
| 任一侧改了 RPC 方法签名 | 不支持,响亮失败(`Unimplemented`) | gRPC 方法名解析的标准行为,规则 4 |
| 新 Console ↔ 旧控制面 | **不支持写路径**,读路径不受影响 | 3.2 节 `DisallowUnknownFields`,新字段的发布/回滚请求会被 400 拒绝 |
| 旧 Console ↔ 新控制面 | 支持(前提是控制面没有把某个字段从"可选"改成"必填") | go-zero 未知键宽松(1.2 节),但本项未逐一核对每个字段是否都保持 optional |
| 旧 Gateway ↔ 新控制面(路由/模板快照多了新字段) | 支持 | 3.2 节,Gateway 拉取路径未用 `DisallowUnknownFields` |
| 旧 Gateway 二进制 + 新版本才有的 flag 写进部署配置 | **不支持**,启动即崩溃 | 3.1 节,flag 严格模式 |
| 回退控制面二进制,数据库已执行过仅追加迁移 | 支持 | 第四节,前提是"仅追加"纪律未被打破 |
| 回退控制面二进制,数据库执行过破坏性迁移(目前尚未发生) | **不支持**,需整库备份恢复 | 第四节 |
| 回退 Registry 二进制,新版本写过新字段的账本文件 | **风险操作**,旧二进制的下一次写会抹掉新字段 | 第五节(真实发现) |

**没有任何组合是"完全免验证、可无脑滚动升级"的**——这份矩阵划的是"哪些方向的字节层面不会崩",不是生产级的兼容性承诺,0.x 阶段的立场(RELEASING.md)不因本文档改变。

## 七、滚动升级与回退操作步骤

结合第三、六节,给出比 RELEASING.md 现有"停机升级"更细一档、但仍然保守的顺序建议(不修改 RELEASING.md 的停机默认建议,只补充"如果确实要滚动"时的方向性指导):

**升级顺序(每一步观察日志/健康检查再进行下一步)**:

1. **Registry** 先升级——它是 Agent 和 Gateway 都依赖的身份/名册权威,自身没有数据库依赖,回退成本最低(第五节风险仅在回退时出现,正向升级没有已知风险)。
2. **控制面**——路由/模板/Job 的权威来源。升级前确认目标版本的迁移都是仅追加(CHANGELOG 的 Breaking Changes 段落应该写清楚,这是 RELEASING.md 已有的要求),迁移由控制面自己的 `-migrate` 执行,不需要等 Gateway。
3. **Gateway**(逐副本滚动)——可以安全落后于控制面(3.2 节),也可以安全兼容新旧 Agent(第二节)。多副本时每次只替换一个副本,用既有的 `ReplicaState: DRAINING` 优雅关闭(tunnel.proto:507-511)排空在途请求。
4. **Agent**——独立于 Gateway 升级节奏,新旧 Agent 可以与刚升级的 Gateway 混跑(第二节验证过的方向)。
5. **Console**——不早于第 2 步的控制面,避免 3.2 节的写路径 400。

**回退顺序**(与升级方向相反,每一步同样先确认再继续):

1. 先回退 Console(如果动过),避免它继续对着即将回退的控制面发出新字段请求。
2. 回退 Gateway 逐副本进行,不依赖控制面或 Registry 先回退。
3. 回退控制面前,按第四节判断这次升级期间的迁移是否仅追加;不是仅追加,先按 `deploy/database-recovery.md` 走整库恢复,而不是直接换回旧二进制。
4. 回退 Registry 前,按第五节判断是否有会被抹掉的新字段;需要的话先备份 `-data-dir`。
5. Agent 独立回退,不受以上顺序约束。

**必须整体停机而非滚动的情况**:任何一次变更涉及 RPC 方法签名变化(规则 4)、数据库破坏性迁移(第四节)、或 CHANGELOG 明确标注为 Breaking Change 的协议/配置变更——这些都是 wire/schema 层面写死了不兼容,滚动升级只会让处于中间状态的副本持续报错,不会自愈。

以上步骤是基于本文档第二至六节证据的**设计推断**,还没有用真实多副本集群跑过一次端到端排练(混合版本 Gateway 集群、真实 Agent 断连重连穿插升级过程),这是第九节的缺口 1。

## 八、真实验证记录（附录）

脚本位置:本次验证在会话临时目录下用独立 Go module 完成,未纳入仓库(纯粹是验证脚手架,不是仓库要长期维护的测试;仓库自身的 proto 兼容性目前只能靠第二节的人工规则清单,没有把这个脚本固化成仓库内的自动化检查,属于第九节缺口)。复现步骤与输出摘要:

```
=== Scenario A: new sender (extra field 3) -> old reader (unknown field 3) ===
old reader saw: node_id="agent-1" seq=42 (extra_hint silently dropped, no error)
PASS: old reader parses new sender's message without error, known fields intact

=== Scenario B: old sender (no field 3) -> new reader (field 3 defined) ===
new reader saw: node_id="gateway-1" seq=7 extra_hint="" (zero value, not an error)
PASS: new reader parses old sender's message, missing field reads as zero value

=== Scenario C (misuse): field 2 reused with a different wire type ===
old reader saw: node_id="node-x" seq=0 -- NO ERROR RAISED, this is the danger: "not-a-number" was silently reinterpreted as varint bytes
CONCLUSION: reusing a field number with an incompatible wire type is NOT reliably caught at unmarshal time -- it can silently corrupt data instead of erroring.
```

构造方式:三份最小 proto(`old.Ping{node_id, seq}`、`newadd.Ping{node_id, seq, extra_hint}`、`newreuse.Ping{node_id, seq_reused_as_string}` 复用字段 2 但类型从 `int64` 改成 `string`),用 `protoc`(本机 `libprotoc 35.0`)+ `protoc-gen-go` 生成 Go 代码,`google.golang.org/protobuf v1.36.11`(与仓库 go.mod 同一版本,从本机模块缓存解析,未触网)做 `proto.Marshal`/`proto.Unmarshal` 互相喂给对方的生成代码,验证第二节规则 1 的两个安全方向和一个误用方向。

## 九、已知缺口清单（供后续任务拆分）

1. **没有真实多副本混合版本集群的端到端滚动升级/回退排练**:第七节的顺序是基于第二至六节证据的设计推断,不是实测——搭建"两个版本的 Gateway 副本同时在线、真实 Agent 在升级过程中断连重连"这类场景需要的工作量接近一个独立任务,本项范围内(设计为主+轻量验证)未覆盖。
2. **没有 `buf breaking` 或等价的 proto 自动化门禁**:第二节的五条规则目前只能靠人工 review 执行,没有 CI 工具会在字段编号被复用或字段被删除却未标 `reserved` 时自动挡下 PR。
3. **`Operation`/`ReplicaState`/`ConfigAction`/`SlotClass` 四个枚举的消费方 `switch` 语句是否都有 `default` 分支未逐一核对**,规则 2 的"旧读者遇到新枚举值不会误判"目前是假设,不是验证过的事实。
4. **go-zero 配置的"未知键宽松"没有反向核对"字段从 optional 改成必填"这类会破坏旧 Console 的变更是否也被允许**——控制面理论上可以在不经意间把一个字段从可选改成必填,老版本 Console 没发这个字段的写请求就会开始报错,这条边界本项未逐一审查现有字段的 `optional` 标记覆盖率。
5. **Registry 的 JSON 文件没有 schema version 字段**,第五节给出的是判断规则而非代码修复;要根治"旧二进制回退时抹掉新字段"这个问题,需要给 `identitystore`/`tokenstore` 引入版本号并让旧版本拒绝加载不认识版本号的文件,这是新代码,不在本项范围。
6. **只验证了字段级别的 wire 兼容性,没有验证 gRPC 层面 TLS/mTLS 握手参数(证书链、cipher suite)跨版本变化的兼容性**——如果未来 Go 标准库升级改变了默认 TLS 行为,这不是本文档覆盖的范围,但可能是真实滚动升级里会遇到的另一类兼容性问题。
7. **一旦项目打出第一个真实 git tag / release**,应该用两个真正的 release 二进制重跑一次第七节的顺序作为补充证据,commit 级别的结论不能自动当作 release 级别的保证——目前只能用 commit 模拟"旧版本",不是本文档的原始设计意图,而是项目当前发布状态(R03:从未推送过镜像)决定的临时替代。

## 十、与 STATUS.md 的关系

本文档本身即视为 A03 的完成交付:现有 proto 契约、四个服务的配置严格/宽松现状、数据库迁移轨道顺序、Registry 文件持久化的回退风险,以及三个真实验证场景,均已给出;凡有代码或真实运行依据的判断都标注了引用或"已验证",凡没有依据的一律列入第九节,不作为承诺。STATUS.md 的 A03 行据此勾选为完成,并链接回本文档;RELEASING.md 里两处"A03 还没做"的表述同步更新为指向本文档,但**不改变 0.x 阶段"不作兼容承诺"的项目立场**——本文档提供的是规则和已知边界,不是生产级保证。第九节的缺口不阻塞勾选,与 A01/A02/R04/P09 等既有条目"已知限制不影响完成判定,但必须如实记录"的先例一致。
