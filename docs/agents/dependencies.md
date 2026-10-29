# 依赖边界与选型理由

新增或调整第三方依赖前读取本文件。`go.mod` 是全仓库的模块清单，不代表每个服务都可以使用其中的所有依赖；同时检查目标服务与它引入的 `common/` 包。

## 通用规则

- 标准库能满足需求时不增加第三方依赖。
- 新增依赖说明解决的问题、标准库方案不足之处、服务归属与新增传递依赖成本。
- 既有例外限定在对应功能内，不据此扩大其他服务或包的依赖边界。
- 版本以 `go.mod` / Console `package.json` 和锁文件为准，本文件不维护版本副本。

## Agent 与 Registry

基础依赖边界为 gRPC、protobuf 与 `github.com/coder/websocket`；WebSocket 用于共享的 ComfyUI 适配器。控制面的框架、ORM、JWT 与密码哈希库不进入 Agent/Registry。

### Agent 本地 YAML 配置例外

`service/aiServeWeaveAgent/agentconfig/` 使用 `github.com/spf13/viper` 读写本地 YAML，并直接使用 `github.com/go-viper/mapstructure/v2` 配置 YAML 标签和模型列表解码钩子。

viper 是团队明确要求的选择。当前只用它完成本地文件读写与解码，不使用 flag 绑定、环境变量绑定或文件热监听；Agent 仍用标准库 `flag`，运行时热重载由既有调和逻辑负责。viper 带入 afero、pflag、cast、fsnotify 等额外模块，这些成本应如实保留在选型说明中；它不是实现本地配置的唯一技术方案，也不因本次整理更换解析库或配置格式。

这项例外只适用于本地文件，不能用于隧道解码，也不改变 `common/tunnelwire` 的转换边界。

## Gateway

除上述数据面基础依赖外，当前例外为：

| 依赖 | 使用范围与理由 |
| --- | --- |
| `github.com/redis/go-redis/v9` | 共享租户限流计数及 Redis 会话存储；跨副本额度不能靠各副本独立计数保证 |
| AWS SDK for Go v2（含 credentials、service/s3）与 `github.com/aws/smithy-go` | `objectstore/` 的 S3 兼容存储与错误处理 |
| `github.com/studio-b12/gowebdav` | `objectstore/` 的 WebDAV 存储，支持仅提供 WebDAV 的 NAS |

Redis 与对象存储是按配置启用的能力；未配置时的退回行为和副本边界见 [Gateway STATUS](../../service/aiServeWeaveGateway/STATUS.md)。`controlplaneclient/` 继续使用标准库 `net/http`，不引入控制面框架或 ORM。

## ControlPlane

go-zero、gorm 及数据库驱动、golang-jwt、`golang.org/x/crypto` 归控制面。其 Admin API、数据库事务、JWT/Redis 可吊销会话与密码哈希需求，不适用于数据面热路径；详细取舍见 [ControlPlane STATUS「为什么这个服务用 go-zero，而数据面不用」](../../service/aiServeWeaveControlPlane/STATUS.md#为什么这个服务用-go-zero而数据面不用)。

`github.com/dodopayments/dodopayments-go` 归控制面，且只由 `internal/billing/dodo` import：结账、客户门户与 Standard Webhooks 验签由官方 SDK 维护，随之带入 standard-webhooks 与 tidwall/gjson、sjson、match、pretty。Console 不引入 npm `dodopayments`、不持有 Dodo 凭据。理由见 [ControlPlane STATUS「计费与充值」](../../service/aiServeWeaveControlPlane/STATUS.md#计费与充值)。

## Console

前端依赖、TypeScript 双版本、Node 内置单元测试与独立 Playwright 验收遵守 [Console AGENTS](../../service/aiServeWeaveConsole/AGENTS.md)。不要把后端的依赖允许列表套用到前端，也不要以升级名义移除其已有兼容安排。
