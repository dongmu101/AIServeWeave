# AIServeWeave Console

AIServeWeave 的 Web 管理控制台，提供租户管理、只读机群与模型目录、工作流目录和 Job 页面。整体架构见 [项目 README](../../README.md)。

**当前已完成 M1–M3、M4 的只读 C21/C23，以及 M5 的 C25 只读目录与 C26 Job 页面。** 包括登录、服务端会话、受限接口转发、用户、API Key、审计、配额、工作流、实时 Job、持久化历史与详情，以及 Job 取消和产物预览/下载。节点管理、路由与模板发布、指标、检索和告警仍待开发；Console 尚无 Job SSE 订阅。详细边界与验收记录见 [STATUS.md](STATUS.md)。

## 技术栈

| 用途 | 选择 |
| --- | --- |
| 框架 | Next.js 16 App Router + React 19 |
| 样式与组件 | Tailwind CSS 4 + shadcn/ui，组件源码在 `components/ui/` |
| 表格 | TanStack Table + TanStack Virtual |
| 图表 | ECharts + `echarts-for-react` |
| 包管理 | pnpm，版本以 `package.json` 的 `packageManager` 为准（当前为 12.3.4） |
| 类型检查 | TypeScript 6 / 7 并存，分别服务 ESLint/构建与日常类型检查 |
| 测试 | Node 24 内置 `node:test`，无额外依赖 |

TanStack Table 用于三张列表，TanStack Virtual 用于审计长列表；ECharts 已安装但尚无指标接口可画。版本与脚本以 [package.json](package.json) 为准。

## 本地开发

准备兼容当前 Next.js 与 pnpm 版本的 Node.js 环境，并使用项目指定的 pnpm 版本。在仓库根目录执行：

```bash
cd service/aiServeWeaveConsole
pnpm install
pnpm dev
```

打开 [http://localhost:3000](http://localhost:3000)，会跳转到登录页。**登录需要一个可用的控制面**：按 [.env.example](.env.example) 复制出 `.env.local` 并设置 `AISW_CONSOLE_CONTROL_PLANE_URL`，再按下文准备后端。不设置时默认连 `http://127.0.0.1:8090`。

开发环境未设置 `AISW_CONSOLE_SESSION_SECRET` 时会生成进程级临时密钥并打印告警，重启开发服务器后已登录会话失效；生产必须显式配置。启动 Console 本身不需要数据库或 GPU。

当前布局通过 `next/font/google` 加载 Geist 字体；首次开发编译或构建需要能够获取字体资源。遇到字体下载失败时，应检查构建环境的网络条件。

## 与 AIServeWeave 的对接

### 调用边界

Console 的业务后端是 ControlPlane Admin API，调用链如下：

```text
浏览器 → /api/session 或 /api/admin/*（Console 的 Next.js 服务端） → ControlPlane Admin API
```

Next.js 服务端负责会话 Cookie 与受限的接口转发，控制面负责身份验证、租户隔离和业务授权。运维读取另走 `/api/operator/*` 与部署密钥。Job 取消与产物访问是唯一的数据面例外：浏览器调用 `/api/gateway/*`，Console 服务端使用会话 Cookie 中加密保存的租户 Gateway Key 转发；Key 不返回浏览器 JavaScript，权限未收窄，详见 [AGENTS.md](AGENTS.md#与后端的边界)。Console 不直连 Registry、Agent、推理后端或数据库。

这一层的三条性质是刻意的，不是实现细节：

- **浏览器不持有控制面令牌。** 令牌只在 `/api/session` 取得，用 AES-256-GCM 密封进 HttpOnly Cookie（生产为 Secure、SameSite=Lax，有效期跟随控制面签发的令牌），由服务端在转发时附加。前端代码里没有控制面地址。
- **`/api/admin/*` 不是通用代理。** 可转发的方法、路径与查询参数由一张白名单决定（`lib/console/upstream-routes.ts`），表外的请求返回 `404`；`POST /admin/v1/auth/login`、租户引导与 Gateway 的 `/internal/v1/apikeys/verify` 都不在表内。
- **写操作校验来源。** CSRF 防护是 SameSite=Lax Cookie 加 `Origin`/`Sec-Fetch-Site` 同源检查；反向代理必须原样透传 `Host`，否则所有写操作会被判为跨源。

控制面本地默认地址是 `http://127.0.0.1:8090`，Admin API 前缀为 `/admin/v1`。服务端配置项见 [.env.example](.env.example)，全部不带 `NEXT_PUBLIC_` 前缀。

### 已有后端能力

| 功能 | 接口 | Console 页面 | 当前边界 |
| --- | --- | --- | --- |
| 登录 | `POST /admin/v1/auth/login` | `/login` | 返回会话令牌、过期时间与用户信息；没有续期或登出接口 |
| 用户 | `GET/POST /admin/v1/users` | `/console/users` | 本租户列表，支持 `role`、`q` 筛选；仅 owner 可创建；未提供编辑、删除、禁用与改密接口 |
| API Key | `GET/POST /admin/v1/apikeys`、`DELETE /admin/v1/apikeys/:id` | `/console/keys` | 列表支持 `status`、`q` 筛选；owner/admin 可创建、吊销；member 只能吊销自己创建的 Key。吊销不幂等：重复吊销返回 `404` |
| 配额 | `GET /admin/v1/tenants/current`、`PUT /admin/v1/tenants/limits` | `/console/quota` | 任何登录角色可读租户资料与配额；仅 owner/admin 可整组写入。零表示不限制 |
| 审计 | `GET /admin/v1/audit` | `/console/audit` | 支持 `action`、`actor_id`、`since`、`until` 筛选与游标分页 |
| 机群清单 | `GET /operator/v1/nodes`、`GET /operator/v1/models` | `/console/fleet`、`/console/models` | **不是租户接口**：节点是所有租户共用的基础设施，由独立的运维密钥守卫。只读，且只在配置了运维模式的部署中存在 |
| 工作流菜单 | `GET /admin/v1/workflows` | `/console/workflows` | 租户接口，返回模板与输入声明；**不含工作流的图**。控制面在返回前清空副本 id 与内部 endpoint |
| 当前运行 | `GET /admin/v1/jobs` | `/console/jobs` | 租户接口，**实时视图不是历史**：网关的 Job 表在内存、有上限、每副本各自持有。不含运行位置，产物只给 id |
| Job 历史 | `GET /admin/v1/jobs/history`、`GET /admin/v1/jobs/history/:id` | `/console/jobs/history` | MySQL 持久化快照；不依赖 Fleet；数据面下载仍受 Gateway 内存映射与原节点可达性限制 |
| 取消与产物 | `POST /v1/jobs/{id}/cancel`、`GET /v1/jobs/{id}/artifacts`、`GET /v1/artifacts/{id}` | 实时 Job 与历史详情 | Console 服务端使用租户 Key 直连 Gateway，需在 `/console/settings` 配置 |
| 发布状态 | `GET /operator/v1/workflows` | `/console/workflows`（运维区块） | 同一份目录，保留「哪些副本注册了它、是否一致」 |

三个列表端点返回 `{ "items": [...], "next_cursor": "..." }` 信封而不是裸数组：`next_cursor` 缺席表示末页，**接口不提供总数**。分页是基于 `(created_at, id)` 倒序的 keyset 游标，`limit` 默认 50、上限 200。契约细节见 [控制面 README](../aiServeWeaveControlPlane/README.md#列表分页)。

API 的字段与状态码以控制面的 [请求响应结构](../aiServeWeaveControlPlane/internal/types/types.go)、[路由](../aiServeWeaveControlPlane/internal/handler/routes.go) 和业务逻辑为准。用户、Key 与审计列表均返回上述分页信封，吊销成功返回 `204`，错误体为 `{ "error": "..." }`。

节点和模型只读目录、工作流与 Job 页面已有接口；节点管理、路由/模板发布与监控仍需补齐管理 API。Gateway 已有推理和 Job API，并不代表这些功能已经具备 Console 管理入口；具体依赖见 [STATUS.md](STATUS.md)。

### 准备后端联调环境

1. 按 [ControlPlane README](../aiServeWeaveControlPlane/README.md) 启动数据库、可选 Redis 与控制面，或按 [部署说明](../../deploy/README.md) 启动完整后端编排。
2. 由运维按部署说明创建第一个租户及 owner。租户引导使用 BootstrapToken，常规 Console 会话不持有它。
3. 以该 owner 登录 Console，走完「创建用户 → 创建 Key → 关闭一次性明文 → 吊销 → 查询审计 → 退出」。

一次可复制的本地联调（不含 Redis，控制面会退回每次校验都查库）：

```bash
docker run -d --name aisw-pg -p 5432:5432 \
  -e POSTGRES_USER=aisw -e POSTGRES_PASSWORD=aisw -e POSTGRES_DB=aiserveweave postgres:17-alpine

# 仓库根目录：把 etc/controlplane.yaml 的 Redis.Addr 改成空串后启动
AISW_ACCESS_SECRET=$(openssl rand -base64 32) \
AISW_INTERNAL_TOKEN=$(openssl rand -base64 32) \
AISW_BOOTSTRAP_TOKEN=$(openssl rand -base64 32) \
  go run ./service/aiServeWeaveControlPlane -f <改过的配置>

curl -XPOST http://127.0.0.1:8090/admin/v1/tenants \
  -H "Authorization: Bearer $AISW_BOOTSTRAP_TOKEN" \
  -d '{"name":"Acme","owner_email":"owner@acme.test","owner_password":"correct-horse-battery"}'

# Console 目录
AISW_CONSOLE_CONTROL_PLANE_URL=http://127.0.0.1:8090 \
AISW_CONSOLE_SESSION_SECRET=$(openssl rand -base64 32) \
AISW_CONSOLE_COOKIE_SECURE=false pnpm dev
```

`AISW_CONSOLE_COOKIE_SECURE=false` 只用于本地明文 HTTP；生产不要设置它。

`AISW_ACCESS_SECRET`、`AISW_INTERNAL_TOKEN`、`AISW_BOOTSTRAP_TOKEN` 属于控制面部署配置，不是浏览器配置，不能通过 `NEXT_PUBLIC_*` 暴露。Gateway 校验用的 `/internal/v1/apikeys/verify` 不属于 Console 可调用接口。

## 目录

```text
app/login/           登录页与登录表单
app/console/         受保护的管理布局、外壳与概览页
app/console/users/   用户列表与创建用户
app/console/keys/    API Key 列表、创建（一次性明文）与吊销
app/console/audit/   管理审计（TanStack Table + Virtual 虚拟滚动）
app/console/quota/   租户资料与配额读写
app/console/fleet/   机群节点（运维模式）
app/console/models/  模型与部署目录（运维模式）
app/console/workflows/ 工作流菜单（租户）+ 副本一致性（运维模式）
app/console/jobs/    当前运行（租户，实时视图）
app/console/jobs/history/ 持久化历史与详情、图片/视频产物预览
app/console/settings/ 租户 Gateway Key 设置
app/api/gateway-key/ Gateway Key 的会话内加密保存与清除
app/api/gateway/     Job 取消、产物列举与流式下载
app/api/session/     登录与退出：唯一取得控制面令牌的地方
app/api/admin/       受白名单限制的 Admin API 转发入口（租户会话）
app/api/operator/    受白名单限制的机群清单转发入口（部署密钥）
proxy.ts             未登录访问控制台页面时的乐观跳转（非授权检查）
lib/console/         浏览器与服务端共用的纯逻辑：契约校验、会话密封格式、
                     转发白名单、来源校验、请求客户端、错误分类、角色矩阵、
                     展示与校验规则、游标分页逻辑（含 *.test.ts）
lib/server/          仅服务端：配置、会话 Cookie 读写、控制面调用、响应封装
components/console/  共享交互：加载/空/失败状态、确认框、提交按钮、表格外壳、
                     翻页控件，以及读取、分页、URL 筛选同步三个 hook
components/ui/       已引入的 shadcn/ui 组件源码
lib/utils.ts         共享 cn 工具导出
public/              静态资源
next.config.ts       Next.js 配置
.env.example         服务端环境变量示例（不含密钥）
package.json         依赖与开发脚本
AGENTS.md            前端开发约定、请求链路约束与 TypeScript 双版本说明
STATUS.md            当前状态、分阶段任务与验收标准
```

## 检查与构建

在 Console 目录执行以下全部命令：

```bash
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

`pnpm typecheck` 使用 TS 7；ESLint 与 Next.js 构建内部使用 TS 6。因此类型检查通过不等于构建通过，不能删掉其中一道门禁。双版本的原因和维护方式见 [AGENTS.md](AGENTS.md)。

`pnpm test` 走 Node 24 内置的 `node:test`，不引入测试框架依赖，覆盖契约解析、会话密封、转发白名单、来源校验与请求层的重试与错误分类；不依赖真实控制面、网络或时钟。组件与页面属于人工验收。`pnpm typecheck` 依赖 `.next/types`，新增路由后先跑一次 `pnpm build`。

构建成功后，可在同一目录启动生产模式服务：

```bash
pnpm start
```

这只启动 Console。使用 Docker 时，[deploy](../../deploy/README.md) 已包含 `console` 服务，
会通过容器网络连接控制面；从仓库根目录也可单独构建镜像：

```bash
docker build -t aisw-console ./service/aiServeWeaveConsole
```

镜像使用 Node.js 24、项目指定的 pnpm 与冻结锁文件构建，启用 `output: "standalone"`，
以非 root 用户执行 `node server.js`。静态资源随镜像发布，`.env*`、本地依赖和构建产物
由 `.dockerignore` 排除；会话密钥和上游地址在运行时注入。

生产部署需要：

- 设置 `AISW_CONSOLE_SESSION_SECRET`（至少 32 字符，未设置时首次需要配置的请求会失败；Compose 会在启动前检查是否为空）与 `AISW_CONSOLE_CONTROL_PLANE_URL`。
- 在 HTTPS 之后运行并启用 Secure Cookie；Compose 为本地回环 HTTP 默认设置 `AISW_CONSOLE_COOKIE_SECURE=false`，对外 HTTPS 部署需改为 `true`。
- 反向代理原样透传 `Host`，否则写操作的同源检查会全部拒绝。

初始密码不限制长度或字符，可以留空；创建用户和登录时都按原值提交。此规则只适用于用户密码，部署会话密钥仍按独立配置要求设置。

## 开发约定

- 修改前阅读 [Console AGENTS.md](AGENTS.md) 和 [仓库 AGENTS.md](../../AGENTS.md)；编写 Next.js 代码前查阅本地 `node_modules/next/dist/docs/` 中对应版本的说明。
- API Key 列表使用后端返回的 `display`，不自行拼接。明文仅在创建成功当次展示，关闭后丢弃；密码、令牌、鉴权头、完整 Prompt 与工作流 JSON 不进入日志或错误提示。
- 页面按钮权限用于交互提示，实际授权始终由后端执行；不根据资源 `404` 推断其他租户的数据是否存在。
- 大列表使用虚拟滚动并限制加载量；事件逐条消费、离开页面取消订阅，产物采用流式传输，避免无界缓冲。
- 新增一个 Admin API 调用，必须同时往 `lib/console/upstream-routes.ts` 的白名单加一行并补测试；不加则请求返回 `404`。控制面改动响应字段时同步改 `lib/console/contract.ts` 的校验。
- 读请求可重试，写请求绝不自动重试：重发一次创建 Key 会铸出第二个凭据。结果未知时改为重新读取列表。
- 列表的筛选与翻页都在控制面执行。新增筛选参数要同时加进 `lib/console/upstream-routes.ts` 的白名单，否则会被静默丢弃。
- 游标只存在于组件状态，不写进 URL：它指向一份特定筛选下的列表，跨筛选变更就失效了。URL 里只放筛选条件，且不放能标识个人身份的输入。
- 接口不返回总数，界面只显示页码，不显示总页数，也不用已加载条数去推算。
- 读取失败不得画成「零条记录」，两者用 `components/console/states.tsx` 中不同的状态组件表达。
- 业务实现、接口变化和验收结果同步更新本 README 与 [STATUS.md](STATUS.md)，区分已完成能力和规划任务。
