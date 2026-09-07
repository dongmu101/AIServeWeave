# aiserveweave-registry

控制面：签发节点证书，维护 Gateway 副本名册。这两件事都要求强一致的一次性状态（bootstrap token 不能被重放、CA 私钥不能泄露），所以只由 Registry 一个进程持有，不下发给任何 Gateway 副本——见顶层 [README.md「安全设计」](../../README.md#安全设计)。

**当前进度：`NodeIdentity`、`GatewayDirectory`、`TokenAdmin` 均已实现。** `NodeIdentity`/`GatewayDirectory` 是第一阶段最后补上的两块：Agent 侧的 bootstrap/续期客户端代码（`service/aiServeWeaveAgent/tunnel/identity.go`）此前已经写好并测试过，只是没有真的 Registry 可以对接；Gateway 侧的 `tunnelserver.Server.SetRoster` 也是现成的注入点，只是没有调用方。`TokenAdmin`（受控签发、撤销、node_id 绑定，STATUS.md 的 S02）是补上的第三块，见下文「`TokenAdmin`」一节；`GatewayDirectory.Join` 的认证与节点禁用/证书吊销（STATUS.md 的 S03）是第四块，见下文「`GatewayDirectory.Join` 的认证（S03）」与「节点禁用与证书吊销（S03）」两节。

| 目录/文件 | 状态 | 内容 |
| --- | --- | --- |
| `internal/ca` | 已实现 | Registry 自己的证书颁发机构：加载或生成根证书、签发节点证书与 Registry 自己的服务端证书 |
| `internal/tokenstore` | 已实现 | 一次性 bootstrap token 的存储：铸造（含可选 node_id 绑定）、消费、撤销、过期与重放校验 |
| `internal/identitystore` | 已实现 | `node_id` 唯一性账本（S01）：记录每个 `node_id` 最近绑定的公钥指纹，供 `Register` 分辨新注册、无害重连与身份冲突；S02 之后，一枚 node_id 绑定的令牌可以让 `Register` 跳过冲突检查，直接覆盖账本；S03 之后同一条记录还携带禁用标记，供 `Disable`/`Enable`/`IsDisabled`/`DisabledNodeIDs` 使用 |
| `internal/registryserver` | 已实现 | `NodeIdentity`（`Register`/`RenewCertificate`）、`GatewayDirectory`（`Join`，S03 起需要 `-gateway-token-file` 认证）与 `TokenAdmin`（`MintToken`/`RevokeToken`/`DisableNode`/`EnableNode`）三个 gRPC 服务的实现 |
| `main.go` | 已实现 | 装配 gRPC 监听 + `-mint-token`/`-revoke-token`/`-disable-node`/`-enable-node`（`TokenAdmin` 的 gRPC 客户端）CLI 工具 |

## 证书与 token 存放在哪

`-data-dir`（默认 `./data/registry`）下：

```
<data-dir>/ca/ca-key.pem     根私钥，0600，永不出这台机器
<data-dir>/ca/ca-cert.pem    根证书，0644，就是 Agent/Gateway 配置里要用的 CA bundle
<data-dir>/tokens.json       bootstrap token 的一次性使用记录，0600
```

根证书首次启动时自动生成（ECDSA P-256，10 年有效期）；之后每次启动直接加载同一份。节点证书由 `internal/ca.CA.Sign` 签发，`URIs` 携带 `aiserveweave://node/<node_id>` SAN（`common/nodeid.URI`），`ExtKeyUsageClientAuth`，30 天有效期——这个签发逻辑照抄自 `tunnel/identity_test.go` 里 `fakeRegistry` 的做法，因为那段代码本来就是"Registry 该怎么签"的规范说明；`internal/registryserver` 的测试直接把 `tunnel.IdentityManager`（Agent 的真实客户端代码）当客户端跑一遍完整流程，而不是自造一个假客户端，为的是证明这里签出来的证书确实能被 Agent 现有代码验证通过。

Registry 自己的 gRPC 监听默认也用这同一个根证书自签一张服务端证书（`-tls-host` 指定 SAN，默认取 `-addr` 的 host 部分），生产环境可以用 `-tls-cert`/`-tls-key` 换成外部签发的证书。

## `TokenAdmin`：受控签发、撤销与 node_id 绑定（S02）

顶层 README 把"控制台生成 bootstrap token"列为尚未开始的组件（`aiserveweave-console`）。在它落地之前，同一个二进制加 `-mint-token`/`-revoke-token` 标志就是发 token 与撤销 token 的方式——但两者都是对正在运行的 Registry 发起的 gRPC 调用（`tunnelv1.TokenAdmin` 服务），不是直接读写 `tokens.json`：

```bash
# server 进程需要先带上 -admin-token-file 才会挂载 TokenAdmin；留空则该服务不注册。
aiserveweave-registry -data-dir ./data/registry -admin-token-file ./data/registry/admin-token

# 另一个终端/主机上铸造一枚 15 分钟有效的令牌：
aiserveweave-registry -data-dir ./data/registry -admin-token-file ./data/registry/admin-token \
  -mint-token -ttl 15m -registry-addr 127.0.0.1:9090
```

打印一个 token 到标准输出，运维把它写进新节点的 `bootstrap-token-file`（配合 Agent 的 `-registry`/`-ca-file`/`-bootstrap-token-file` 三个 flag，见 `service/aiServeWeaveAgent/main.go`）。`-registry-addr` 留空时从 `-addr` 推导（同机同端口的默认情形）。撤销同理：

```bash
aiserveweave-registry -data-dir ./data/registry -admin-token-file ./data/registry/admin-token \
  -revoke-token <token值> -registry-addr 127.0.0.1:9090
```

**为什么改成 RPC，而不是给旧的文件访问模式加一把跨进程锁。** 撤销必须让正在运行的 server 立刻感知——它把 token 状态保存在内存里，一把文件锁只能防止两次写入互相覆盖，防不住 server 读到的还是撤销前的内存状态。铸造和撤销因此走同一条受管控的路径：`Store` 的互斥锁与内存状态只由持有它的那个进程（server）安全地变更，CLI 模式不再打开 `tokens.json`，`-mint-token`/`-revoke-token` 只是 `TokenAdmin` 的瘦客户端。这也顺带解决了旧模式"CLI 与 server 共享同一份 token 文件、极小概率互相覆盖"的已知限制——不是给它打了补丁，而是它已经不存在了。

**认证**：`TokenAdmin` 用一个静态共享密钥（`-admin-token-file`，与 `-bootstrap-token-file` 一样是路径而不是明文 flag，避免出现在进程列表/shell 历史里；至少 32 字节，和控制面 `OperatorToken` 的门槛一致）挂在 gRPC metadata 的 `authorization: Bearer <token>` 上，常数时间比较——这是 Gateway `adminapi` 与控制面 `/operator/v1/*` 已经在用的同一个 Bearer token 约定，搬到 gRPC metadata 上而已，不是第三种方案。

**node_id 绑定**：`-mint-token` 可以带 `-bind-node-id <node_id>`，铸造一枚只对这个 node_id 有效的令牌；见下一节「`node_id` 唯一性」。

## `-issue-server-cert`：Gateway 的隧道证书从哪来

Gateway 的隧道监听器需要一张服务端证书，而它别处拿不到：Agent 只信任一个根——`tunnel/identity.go` 的 `TLSConfig` 把 `-ca-file` 加载的 Registry CA 作为 `RootCAs`，因此任何其他签发方的证书都会被每一个 Agent 拒绝。而本服务通过 RPC 签发的是**节点**证书（`ExtKeyUsageClientAuth` + `aiserveweave://node/<id>` SAN），那不是监听器该出示的东西。

```bash
aiserveweave-registry -data-dir ./data/registry -issue-server-cert \
  -tls-host gateway,127.0.0.1 -out-dir ./certs
```

写出 `server-cert.pem`（0644）与 `server-key.pem`（0600），`ExtKeyUsageServerAuth`，SAN 取 `-tls-host`。与 `-mint-token` 一样是 CLI 模式而不是 RPC，理由相同：这是部署时执行一次的运维动作，执行者本来就对 CA 有文件系统访问权。`deploy/docker-compose.yaml` 的 `registry-init` 就是这条命令。

## `GatewayDirectory`：Gateway 副本怎么拿到名册

Gateway 副本启动时用 `service/aiServeWeaveGateway/registryclient` 拨号 `GatewayDirectory.Join`（一条双向流），报告自己的 `replica_id`/`endpoint`；Registry 把当前所有在线副本的名册广播给每一条打开的流，副本集合或状态变化时重新广播一次，版本号单调递增。副本优雅关闭前会在流上多发一条 `state: DRAINING` 的消息，再关闭连接；连接断开（无论是否发过 DRAINING）都会让该副本立刻从名册里消失并触发一次广播——不保留"已标记 removed 但还留着"的中间态。

这条链路的传输层是单向 TLS：Gateway 校验 Registry 的服务端证书（`-registry-ca`），但不向 Registry 出示客户端证书——Gateway 目前不在 Registry 签发的 mTLS 体系里，这是本次特意收窄的范围；应用层的调用方认证见下一节。

## `GatewayDirectory.Join` 的认证（S03）

S02 之前，`Join` 接受任何能连到 Registry gRPC 监听器的调用方，唯一的门槛是传输层验证 Registry 自己的证书——没有反过来验证调用方是谁。S03 补上了一个独立于 `TokenAdmin.AdminToken` 的共享密钥 `-gateway-token-file`：

```bash
aiserveweave-registry -data-dir ./data/registry -gateway-token-file ./data/registry/gateway-token
```

Gateway 侧对应 `-registry-join-token-file`（`service/aiServeWeaveGateway/main.go`），`registryclient.Run` 把文件内容作为 `authorization: Bearer <token>` 挂在 `Join` 的 gRPC metadata 上（`registryclient.Config.GatewayToken`）——与 `requireAdmin` 校验 `TokenAdmin` 调用方的方式相同，只是换了一把密钥、搬到了 `roster.go` 的 `requireGatewayToken`。

**为什么是独立的一把密钥，而不是复用 `AdminToken`。** 一个 Gateway 副本只需要证明自己有权加入名册，不需要铸造/撤销令牌或禁用节点——把 `AdminToken` 发给每一个副本，会让它的爆炸半径等同于一个运维操作者。两把密钥分别对应两种不同权限的调用方，这是刻意的最小权限划分，不是两套本可以合并的机制。

**空值退回未认证。** `-gateway-token-file` 留空时 `Join` 保持 S03 之前的行为——这与 `TokenAdmin` 在 `AdminToken` 为空时整个服务不注册不同：`GatewayDirectory` 是 Gateway 运行所必需的核心功能，不能像 `TokenAdmin` 那样按需注册/不注册；`requireGatewayToken` 自己在密钥为空时直接放行，而不是拒绝所有调用。

## 节点禁用与证书吊销（S03）

`TokenAdmin` 新增两个方法，`AdminToken` 同样守护：

- **`DisableNode(node_id)`**：把 `node_id` 标记为禁用（持久化进 `internal/identitystore.Store`，与 S01 的公钥指纹账本是同一份记录），此后 `Register` 与 `RenewCertificate` 都会拒绝这个 `node_id`——即便呈递的引导令牌或客户端证书本身仍然有效。可以对一个从未注册过的 `node_id` 提前禁用（例如一枚绑定令牌在被消费前泄漏），这时账本里会创建一条尚无指纹的记录。
- **`EnableNode(node_id)`**：清除禁用标记；对一个当前并未被禁用的 `node_id` 调用不是错误。

**只挡住未来的握手是不够的：既有连接也要生效。** `DisableNode`/`EnableNode` 每次写入后都会用当前完整的禁用集合调用 `rosterState.setRevoked`，把它塞进 `GatewayRoster.revoked_node_ids` 字段，走已有的名册广播通道推给每一条打开的 `Join` 流——不是新起一条推送链路，是把「禁用节点」表达成「名册的一部分变了」，复用 S02 之前就有的分发机制。

Gateway 收到带 `revoked_node_ids` 的名册后（`tunnelserver.Server.SetRoster`）：

1. 记下这份禁用集合，此后每一次新的 `Control`/`Serve` 握手都会先查一次（`isRevoked`），命中就直接拒绝——这挡住了「刚被踢掉立刻用同一张证书重连」的情形，因为证书本身在到期前仍然是密码学有效的，Registry 并不维护 CRL/OCSP。
2. 对已经连接的节点，如果它的 `node_id` 出现在这份集合里，立即调用 `node.kill()`：关闭它全部空闲槽（与 Agent 自己宣告 draining 时的处理一致），并让它的 `Control` 读循环从阻塞的 `Recv` 之外被打断返回——`serveControl` 把 `Recv` 挪进一个 goroutine 用 channel 接住结果，好让主循环能同时 `select` 这个 channel 与节点的 `revoked` 关闭信号；这是"既有连接也要生效"这半句要求的直接后果：单靠 Agent 配合的优雅 Draining 通知做不到强制。正在处理中的忙碌槽不受影响，请求跑完或自然失败——这与 `Server.Close` 在整个副本关闭时已经采用的克制一致，中途掐断数据面的流对等待结果的一方是更差的失败方式。

**这条链路是最终一致的，不是瞬时的。** 从 `DisableNode` 返回到某个 Gateway 副本真正切断连接之间，存在名册广播传播的窗口；一个还没收到最新名册的副本仍会认为该节点合法。这与 S02 之前 `GatewayRoster` 本身传播新副本/状态变化时的一致性模型相同，S03 没有改变这一点，只是把「哪些节点被禁用」纳入了同一份需要传播的状态。

## `node_id` 唯一性（S01）

`Register` 在签发证书之后、把响应交回去之前，会把这次 CSR 携带的公钥指纹（`internal/identitystore.Fingerprint`：DER 编码 `SubjectPublicKeyInfo` 的 SHA-256）与 `internal/identitystore.Store` 账本里 `node_id` 上次绑定的指纹比对，得到三种结果之一：

- **`node_id` 从未出现过。** 记录这次的指纹，正常签发——与此前行为一致。
- **`node_id` 已在案，指纹相同。** 判定为无害的重连（多半是节点从未把上一次签发的证书落盘），照常签发；日志用 `first_registration=false` 与首次注册区分。
- **`node_id` 已在案，指纹不同，且这次请求消耗的是一枚绑定了这个 node_id 的令牌（`TokenAdmin.MintToken` 的 `-bind-node-id`，S02）。** 放行：`Register` 对这种请求调用 `identitystore.Store.Set` 而不是 `Reserve`，无条件用新指纹覆盖账本。授权证明来自运维那次经过 `-admin-token-file` 认证的铸造调用，而不是与账本历史的比对——这正是 S01 当时缺的那个信号。
- **`node_id` 已在案，指纹不同，且令牌未绑定 node_id（或压根没有 S02 之前铸造的旧令牌）。** 拒绝，返回 gRPC `AlreadyExists`；日志按 `Warn` 记录 `node_id`（不记录密钥或指纹本身）。这一种情形仍然不区分"运维在重装这个节点"与"有人在冒用一个不属于自己的 node_id"——一枚未绑定的令牌不携带这个信号，Registry 没有别的可靠依据，硬猜比拒绝更危险。

**运维怎么处理一次合法重装。** 首选路径（S02 之后）：铸造一枚绑定了该 `node_id` 的令牌（`-mint-token -bind-node-id <node_id>`），把它交给重装的节点——不需要碰 `identities.json`。这条路径仍然要求确认操作者身份走带外渠道（工单、内部沟通），只是把"清空账本记录"这一步换成了"铸造一枚范围收紧到单个 node_id 的令牌"，两者都是运维手动、带外确认后的操作，只是前者事后修修补补，后者事前一次性授权。旧路径依然存在，供没有用绑定令牌、直接撞上 `AlreadyExists` 的情形使用：手动编辑 `-data-dir` 下的 `identities.json`，删掉该 `node_id` 对应的那一条记录（或者干脆停止 Registry、备份、编辑、重启——文件是纯 JSON，格式与 `internal/identitystore.Store` 的 `record` 类型一致），再让节点用一枚新引导令牌重新注册。**怎么识别一次冒用尝试**：日志里出现 `"node_id re-registered with a different public key"` 而运维并未授权过重装，就是信号——发生时先吊销/停用对应的引导令牌铸造权限（`-revoke-token`，或直接停用 `-admin-token-file`），再排查令牌是如何泄漏或被猜中的。

签发发生在账本检查之前（见 `identity.go` 的 `Register` 文档注释）：反过来的顺序会让一次注定被拒绝的请求先占住 `node_id`，再在签发上失败，账本就会指向一把从没人证明持有过的 key；现在这个顺序下，一次冲突请求的代价只是白白消耗掉已经花掉的引导令牌换来一张永远不会被交回去的证书。

**并发注册按 `node_id` 序列化。** `identitystore.Store.Reserve` 全程持有一把互斥锁，两个并发的 `Register` 调用即便都携带同一个 `node_id`，也不会让账本产生一次"看到了旧值"的竞态——`internal/identitystore/identitystore_test.go` 与 `internal/registryserver/identity_test.go` 分别在账本层与真实 gRPC 层验证了「同一把 key 并发重连全部成功」与「不同 key 并发抢注恰好一个成功、其余全部拒绝」。

`RenewCertificate` 不走这条检查：它自己的认证（`nodeid.FromPeer` 必须等于请求里的 `node_id`）已经证明调用方持有该 `node_id` 当前的私钥，因此续期时账本用 `identitystore.Store.Set` 无条件覆盖指纹——一次续期常规地会带来一把新生成的 key，若也用 `Reserve` 处理会被误判为冲突。

## 已知限制 / 下一步

- **单实例假设。** bootstrap token 的一次性校验与 `node_id` 身份账本都靠本地文件 + 内存锁保证强一致，这只在只有一个 Registry 进程时成立。顶层 README 路线图把「Registry 和 Gateway 高可用」放在第三阶段，在那之前不要跑多个 Registry 实例。
- **令牌绑定的是 node_id，不是租户。** 这是刻意的：机群是所有租户共用的基础设施，节点本身没有租户维度可言——同样的判断，见控制面 README 关于 `/operator/v1/*` 为什么不放进会话守卫的说明。因此 S02 只做了 node_id 绑定（解决"重装"与"冒用"的分辨，见上文），proto 里 `bootstrap_token` 早先"tenant-bound"的注释已经删掉；哪些人能调用 `TokenAdmin`（今天是持有 `-admin-token-file` 里那把共享密钥的所有人）本身要不要引入租户/角色维度，属于路线图第三阶段的多租户 RBAC。
- **`-admin-token-file`/`-gateway-token-file` 都是不区分调用者的共享密钥。** 和这两把密钥守护的其它管理面（控制面 `OperatorToken`、Gateway `adminapi`）一样，持有对应密钥的任何人都能执行该密钥授权的全部操作，没有按操作者归因的审计轨迹；引入平台级运维身份之前，这是已知且接受的缺口。
- **Gateway↔Registry 仍是单向 mTLS。** S03 给 `Join` 加上的是应用层的共享密钥认证，不是让 Gateway 持有 Registry 签发的客户端证书；要不要升级到 mTLS，等控制面需要更强隔离时再评估。
- **禁用生效依赖名册广播，不是密码学吊销。** `DisableNode` 不会让节点证书本身失效——Registry 不维护 CRL/OCSP，证书在到期前始终密码学有效。它能拒绝任何还认识这个 `node_id` 的 Registry/Gateway 组件，但一个从未连上任何在线副本、或连着一个还没收到最新名册的副本的节点，在那之前不会感知到自己被禁用；这与 `GatewayRoster` 本身的一致性模型相同。
