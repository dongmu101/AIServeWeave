# aiserveweave-registry

控制面：签发节点证书，维护 Gateway 副本名册。这两件事都要求强一致的一次性状态（bootstrap token 不能被重放、CA 私钥不能泄露），所以只由 Registry 一个进程持有，不下发给任何 Gateway 副本——见顶层 [README.md「安全设计」](../../README.md#安全设计)。

**当前进度：`NodeIdentity` 与 `GatewayDirectory` 均已实现。** 这是第一阶段最后补上的两块：Agent 侧的 bootstrap/续期客户端代码（`service/aiServeWeaveAgent/tunnel/identity.go`）此前已经写好并测试过，只是没有真的 Registry 可以对接；Gateway 侧的 `tunnelserver.Server.SetRoster` 也是现成的注入点，只是没有调用方。

| 目录/文件 | 状态 | 内容 |
| --- | --- | --- |
| `internal/ca` | 已实现 | Registry 自己的证书颁发机构：加载或生成根证书、签发节点证书与 Registry 自己的服务端证书 |
| `internal/tokenstore` | 已实现 | 一次性 bootstrap token 的存储：铸造、消费、过期与重放校验 |
| `internal/registryserver` | 已实现 | `NodeIdentity`（`Register`/`RenewCertificate`）与 `GatewayDirectory`（`Join`）两个 gRPC 服务的实现 |
| `main.go` | 已实现 | 装配 gRPC 监听 + `-mint-token` CLI 占位工具 |

## 证书与 token 存放在哪

`-data-dir`（默认 `./data/registry`）下：

```
<data-dir>/ca/ca-key.pem     根私钥，0600，永不出这台机器
<data-dir>/ca/ca-cert.pem    根证书，0644，就是 Agent/Gateway 配置里要用的 CA bundle
<data-dir>/tokens.json       bootstrap token 的一次性使用记录，0600
```

根证书首次启动时自动生成（ECDSA P-256，10 年有效期）；之后每次启动直接加载同一份。节点证书由 `internal/ca.CA.Sign` 签发，`URIs` 携带 `aiserveweave://node/<node_id>` SAN（`common/nodeid.URI`），`ExtKeyUsageClientAuth`，30 天有效期——这个签发逻辑照抄自 `tunnel/identity_test.go` 里 `fakeRegistry` 的做法，因为那段代码本来就是"Registry 该怎么签"的规范说明；`internal/registryserver` 的测试直接把 `tunnel.IdentityManager`（Agent 的真实客户端代码）当客户端跑一遍完整流程，而不是自造一个假客户端，为的是证明这里签出来的证书确实能被 Agent 现有代码验证通过。

Registry 自己的 gRPC 监听默认也用这同一个根证书自签一张服务端证书（`-tls-host` 指定 SAN，默认取 `-addr` 的 host 部分），生产环境可以用 `-tls-cert`/`-tls-key` 换成外部签发的证书。

## `-mint-token`：控制台出现之前的过渡方案

顶层 README 把"控制台生成 bootstrap token"列为尚未开始的组件（`aiserveweave-console`）。在它落地之前，同一个二进制加 `-mint-token` 标志就是发 token 的方式：

```bash
aiserveweave-registry -data-dir ./data/registry -mint-token -ttl 15m
```

打印一个 token 到标准输出，运维把它写进新节点的 `bootstrap-token-file`（配合 Agent 的 `-registry`/`-ca-file`/`-bootstrap-token-file` 三个 flag，见 `service/aiServeWeaveAgent/main.go`）。

**已知限制**：这个 CLI 模式直接读写 `tokens.json`，和正在运行的 server 进程之间没有跨进程锁。两者极小概率同时写文件时后写的会覆盖先写的——单个 token 的铸造/消费都是低频操作，真撞上了重试一次即可，不是安全问题。这条限制和 Agent 侧 `-ollama-url` 之类的过渡 flag 性质一样：控制台落地后这条路径整个替换掉，不需要现在就补跨进程锁。

## `-issue-server-cert`：Gateway 的隧道证书从哪来

Gateway 的隧道监听器需要一张服务端证书，而它别处拿不到：Agent 只信任一个根——`tunnel/identity.go` 的 `TLSConfig` 把 `-ca-file` 加载的 Registry CA 作为 `RootCAs`，因此任何其他签发方的证书都会被每一个 Agent 拒绝。而本服务通过 RPC 签发的是**节点**证书（`ExtKeyUsageClientAuth` + `aiserveweave://node/<id>` SAN），那不是监听器该出示的东西。

```bash
aiserveweave-registry -data-dir ./data/registry -issue-server-cert \
  -tls-host gateway,127.0.0.1 -out-dir ./certs
```

写出 `server-cert.pem`（0644）与 `server-key.pem`（0600），`ExtKeyUsageServerAuth`，SAN 取 `-tls-host`。与 `-mint-token` 一样是 CLI 模式而不是 RPC，理由相同：这是部署时执行一次的运维动作，执行者本来就对 CA 有文件系统访问权。`deploy/docker-compose.yaml` 的 `registry-init` 就是这条命令。

## `GatewayDirectory`：Gateway 副本怎么拿到名册

Gateway 副本启动时用 `service/aiServeWeaveGateway/registryclient` 拨号 `GatewayDirectory.Join`（一条双向流），报告自己的 `replica_id`/`endpoint`；Registry 把当前所有在线副本的名册广播给每一条打开的流，副本集合或状态变化时重新广播一次，版本号单调递增。副本优雅关闭前会在流上多发一条 `state: DRAINING` 的消息，再关闭连接；连接断开（无论是否发过 DRAINING）都会让该副本立刻从名册里消失并触发一次广播——不保留"已标记 removed 但还留着"的中间态。

这条链路目前是单向 TLS：Gateway 校验 Registry 的服务端证书（`-registry-ca`），但不向 Registry 出示客户端证书——Gateway 目前不在 Registry 签发的 mTLS 体系里,这是本次特意收窄的范围。

## 已知限制 / 下一步

- **`node_id` 冲突检测缺失。** `Register` 在 `node_id` 非空时直接采用，不检查是否已经有另一个节点在用同一个 ID。顶层 README「待决问题 3」把这个运维口径标注为上线前才需要定的问题，本次不解决。
- **单实例假设。** bootstrap token 的一次性校验靠本地文件 + 内存锁保证强一致，这只在只有一个 Registry 进程时成立。顶层 README 路线图把「Registry 和 Gateway 高可用」放在第三阶段，在那之前不要跑多个 Registry 实例。
- **多租户 token 绑定未实现。** proto 注释里 `bootstrap_token` 的"tenant-bound"目前只是占位描述，token 本身不携带租户信息；多租户 RBAC 是路线图第三阶段的内容。
- **Gateway↔Registry 单向认证。** 见上一节；要不要求 Gateway 也持有 Registry 签发的证书，等控制面需要更强隔离时再评估。
