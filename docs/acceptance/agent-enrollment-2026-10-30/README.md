# Agent 接入材料下载验证（2026-10-30）

本记录验证控制台下载流程与默认测试，不代表远程 Ubuntu/Windows/macOS 节点已完成注册、模型请求或操作系统兼容验收。

## 交付行为

- `/operator/agent-enrollment`：平台运维可下载公开 CA，填写外部 Registry/Gateway 地址和有效期后生成新节点 ZIP；节点提供者无需平台账户或平台服务器权限。
- 每包随机新 node ID，复用现有 Registry `TokenAdmin.MintToken` 的 node 绑定与消费语义；不提供指定既有身份或重装入口。
- ZIP 仅含公共 CA、一次性令牌、连接信息与安装说明；二进制和安装器另由匹配版本发行包提供。生成操作不自动重试、不持久化下载历史，成功后写不含凭据的平台审计。

## 本地验证

环境：macOS arm64；工具链按仓库 go.mod 与 Console 锁文件安装。浏览器为独立 Chrome 测试实例，平台会话与接口为隔离样本。

| 检查 | 结果与范围 |
| --- | --- |
| `go test ./...` | 通过；默认离线测试，不替代真实 Registry/节点部署 |
| `go vet ./...`、`go build ./...` | 通过 |
| `gofmt -l ./service ./api`、`git diff --check` | 无输出 |
| `go generate ./api/...` | 无契约或生成语义变化；本机 protoc 7.35.1 与仓库 7.34.1 仅两处工具版本头不同，未纳入改动 |
| Console 默认测试 | 304 项通过，包括接入响应校验、Operator 白名单隔离和创建失败不重试 |
| Console lint、TS 7/TS 6 类型检查、生产构建 | 通过；lint 保留原有 alerts 页 TanStack Virtual 的一条兼容性警告 |
| 浏览器 `e2e/agent-enrollment.spec.ts` | 3 项通过：1440/375px 下实际下载 CA 和 ZIP、重复下载不重复签发、刷新丢弃凭据；签发失败不自动重试 |
| `go test -race ./service/...` | 未全通过：改动涉及的控制面包通过；未修改的 Agent tunnel `TestManagerDrainAllStopsDispatchAndWaitsForInFlight` 失败，表现为排空后仍有空闲推理连接。单独重复 10 次有 3 次同样失败；本次未修改该包 |

后端新增测试覆盖：缺失/租户/已撤销平台会话拒绝、禁止选择既有 node ID、超大 TTL 拒绝、CA 大小和非证书块拒绝、过期 CA 拒绝、下载摘要、ZIP 固定四文件与令牌 0600、签发 metadata/超时及绑定参数、审计脱敏。测试不使用生产凭据。

## 仍需部署验收

重建 ControlPlane 和 Console 后，以真实平台运维登录下载接入包；在实际远程节点上校验连接地址、服务端证书域名和端口可达性，完成一次性令牌注册与重连，配置本地后端，再验证普通请求和 SSE。CA 下载成功不能证明远端端口或模型推理可用。
