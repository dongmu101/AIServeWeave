## 商户订单删除（2026-11-13）

商户计费页「订单」新增删除与确认框，owner/admin 可删除任意状态的本商户订单；member 只读。删除持久化为 `checkout_orders.tenant_deleted_at`，仅商户列表隐藏，运维历史、付款恢复、去重、发货、退款与库存占用继续使用原订单。新接口为 `DELETE /admin/v1/billing/orders/:id`，成功 `204`，跨商户、未知与已删除统一 `404`，审计动作 `billing.order.delete`。Console 白名单、八语言提示、失败保留确认框与末页回退已接入。迁移为 PostgreSQL/MySQL `billing/0010_order_deletion.sql`，`sql/init.sql` 已同步；控制面与 Console 须一起更新。行为与升级说明见[商户订单删除](../../docs/billing-orders.md)。

验证：控制面默认测试及 logic/handler/store 竞态检查、Console 375 项单元测试、lint（仅既有 alerts 页警告）、类型检查与生产构建、Chrome 计费回归 29 项通过。新增共享 store 场景覆盖所有订单状态、租户隔离、分页前过滤、删除后的迟到付款与重放去重；新增 HTTP/logic 用例覆盖授权与审计。真实 PostgreSQL/MySQL 迁移、真实付款与生产部署尚未验收。

# Console 开发状态与任务规划

更新日期：2026-09-12。M1–M3 已完成；M4 完成 C21–C24，M5 完成 C25、C26、C27（P08）、C28（P09a，请求检索）与 C29（P09b，告警，见 [根 STATUS 的 P09 行](../../STATUS.md)），P05 用户与会话生命周期已接入。本文记录 `service/aiServeWeaveConsole` 的现状、实施顺序、后端依赖与验收标准；任务尚未实现时保持未勾选，不将依赖安装视为业务功能完成。

## 目标与范围

首版面向单个租户内的 owner、admin、member，完成登录、用户列表与创建、API Key 生命周期、管理审计的真实接口闭环。配额读取与编辑已交付。节点、模型、路由、工作流和监控按后端 Admin API 的成熟度逐步接入。

Console 通过服务端调用控制面的 Admin/Operator API；Job 取消与产物访问是已实现的例外，由 Console 服务端持有租户 Gateway Key 直连数据面，见 [AGENTS.md](AGENTS.md#与后端的边界)。不直连 Registry、Agent、推理后端或数据库。租户可由运维经 BootstrapToken 引导，也可在部署开启后经 `/signup` 自助注册（Console 从不持有 BootstrapToken）；不提供跨租户管理或租户切换。

## 当前状态

| 模块 | 状态 | 已核实的内容 |
| --- | --- | --- |
| 工程基础 | 已就位 | Next.js 16 App Router、React 19、Tailwind CSS 4、pnpm |
| UI 基础 | 已就位 | `components/ui/` 已有按钮、输入、表格、对话框、标签页、骨架屏等组件；`lib/utils.ts` 使用 `cn` 包 |
| 表格与图表依赖 | 表格已接入，图表待数据源 | TanStack Table 用于列表、TanStack Virtual 用于审计虚拟滚动；ECharts 与 `echarts-for-react` 尚未接入指标业务 |
| TypeScript 与门禁 | 已配置 | TS 6 / 7 并存；已有 `lint`、`typecheck`、`build` 脚本，不代表本次已运行通过 |
| 页面 | M1 已就位 | 登录页 `app/login/`、受保护布局与概览页 `app/console/`；根布局已改为中文站点元数据 |
| 登录、会话与 API 客户端 | 已落地 | `/api/session` 与 `/api/admin/*` 服务端入口、AES-256-GCM 密封 Cookie、Redis 服务端撤销、`lib/console/api-client.ts` |
| 业务测试 | 已建设 | Console `pnpm test` 走 Node 内置 `node:test`，覆盖契约、权限、分页与 Gateway 凭据等纯逻辑，无新增测试框架依赖；控制面新增分页、筛选、租户读取、机群与工作流聚合的 Go 测试及 e2e 隔离断言，Gateway 新增运维端点测试 |
| 使用文档 | 已重写 | README 记录调用链路、环境变量、门禁与部署前置条件 |
| 业务页面（用户/Key/审计/配额） | M2、M3 已就位 | `app/console/users`、`keys`、`audit`、`quota`，均对接真实 Admin API；三张列表为服务端游标分页与筛选 |
| 运维页面（节点/模型/发布状态） | M4、M5 已就位 | `app/console/fleet`、`models`，以及 `workflows` 的运维区块；只读，只在配置了运维模式的部署中存在，且只对名单内的人存在 |
| 预充值计费 | 已接入 | 租户 `app/console/billing`、运维 `app/operator/(protected)/billing`，经白名单调用控制面计费 API；Console 不持有 Dodo 或一人付凭据，见下文「预充值计费 Console 接入」与「支付宝（一人付）兑换」 |
| 公开首页与法律条款 | 已交付（2026-11-12），未经支付机构审核 | `/` 未登录时为产品介绍首页，新增 `/terms`、`/privacy`、`/refund`，公开页统一页脚、注册同意声明与计费页条款提示；营运方资讯来自 `AISW_CONSOLE_LEGAL_*` 环境变量，未设置时显示占位符。见 [README「公开首页与法律条款」](README.md#公开首页与法律条款) |
| 工作流与运行（租户） | M5 已就位 | `app/console/workflows`（菜单，不含图）、`app/console/jobs`（实时视图）、`app/console/jobs/history`（持久化历史与详情），取消和产物访问经服务端 Gateway 入口 |

实现时遵守 [Console 开发约定](AGENTS.md) 与 [仓库开发约定](../../AGENTS.md)。技术栈沿用现有选型；动 Next.js 代码前先读本地 `node_modules/next/dist/docs/`，不在本规划中引入依赖升级。

## 已有接口与真实边界

接口依据：[路由](../aiServeWeaveControlPlane/internal/handler/routes.go)、[请求与响应结构](../aiServeWeaveControlPlane/internal/types/types.go)、[HTTP 处理](../aiServeWeaveControlPlane/internal/handler/handlers.go)、[用户与权限逻辑](../aiServeWeaveControlPlane/internal/logic/service.go)、[Key 与配额逻辑](../aiServeWeaveControlPlane/internal/logic/apikeys.go)。以下是当前实现，后续接口变化需同步更新。

| 能力 | 已有接口 | Console 接入边界 |
| --- | --- | --- |
| 登录 | `POST /admin/v1/auth/login`、`DELETE /admin/v1/auth/session`、`POST /admin/v1/auth/{password,sessions/revoke}` | Redis 可吊销会话；无 refresh 接口 |
| 用户 | `GET/POST /admin/v1/users`、`PUT/POST /admin/v1/users/:id/*` | 登录用户可读；owner 可创建、重置密码、改角色、禁用/启用与撤销会话 |
| API Key | `GET/POST /admin/v1/apikeys`、`DELETE /admin/v1/apikeys/:id` | 登录用户可读本租户列表；owner/admin 可创建与吊销；member 仅可吊销自己创建的 Key |
| 租户配额 | `GET /admin/v1/tenants/current`、`PUT /admin/v1/tenants/limits` | 全部登录角色可读；owner/admin 可整组写入，零表示不限制 |
| 审计 | `GET /admin/v1/audit` | 全部登录角色可读本租户审计；支持 `limit`、`cursor`、`action`、`actor_id`、`since`、`until`，不提供总数 |
| 租户引导 | `POST /admin/v1/tenants` | 需要 BootstrapToken；不接入常规 Console 会话 |
| 租户自助注册 | `POST /admin/v1/auth/signup` | `/signup` 页面 + `/api/signup` 路由，双开关（控制面 `Signup.Enabled`、Console `AISW_CONSOLE_SIGNUP_ENABLED`）；成功后经与登录共用的 `lib/server/sign-in.ts` 建立会话 |
| 预充值计费 | `GET /admin/v1/billing`、`POST /admin/v1/billing/{checkout,portal}`、`POST /admin/v1/billing/accounts/:id/checkout`、`GET /admin/v1/billing/{ledger,orders}`；运维 `/operator/v1/billing/*` | 支付端点仅在控制面 `Billing.Enabled` 时挂载，否则 `404`；余额、账号和账目也可由 `AccountSales.Enabled` 独立开启；全部登录角色可读，owner/admin 可结账与打开门户（member `403`）；`503` 表示控制面联系 Dodo 失败 |
| Gateway 校验 | `POST /internal/v1/apikeys/verify` | 需要 InternalToken；Console 不调用、不持有该密钥 |

用户、Key 与审计列表返回 `{items, next_cursor}` 信封，支持游标分页与服务端筛选；吊销成功返回 `204`，错误体为 `{ "error": "..." }`。Job 历史另有 `/admin/v1/jobs/history` 与详情端点，不依赖 Fleet 配置。

### R04 文档核对（2026-09-07）

已对照控制面路由、角色逻辑及 Console 转发白名单修正配额读取、列表分页与 Job 历史的过时描述。member 可查看本租户 Key 展示信息、只能吊销自己创建的 Key，不能创建 Key；用户接口只有列表与创建。角色不允许返回 `403`，资源不存在、跨租户或 member 吊销他人 Key 返回 `404`；重复吊销同样返回 `404`。这些边界已在服务 README 中记录。

历史验收记录保留其当时范围；以下阶段中的实测数字不代表本次重新运行。C25 完成的是只读模板目录，模板版本管理与发布仍归根 STATUS 的 P03；C26 的历史与产物页面已接入，但没有 Console SSE 订阅，历史快照和可访问的产物不保证重启后可下载，见 Gateway README 的持久化限制。

## 实施顺序

| 阶段 | 优先级 | 交付目标 | 依赖与退出条件 |
| --- | --- | --- | --- |
| M1 基础与会话 | P0 | 可登录、刷新、退出的控制台框架 | 现有登录 API；通过会话与请求隔离验证 |
| M2 管理闭环 | P0 | 用户、API Key、近期审计 | M1；真实接口完成关键操作与角色验证 |
| M3 配额与数据规模 | P1 | 可读写配额、可分页查询 | 控制面新增读取与分页契约后交付 |
| M4 推理资源管理 | P2 | 节点、运行时、模型、路由 | 对应控制面聚合与管理 API |
| M5 工作流与可观测性 | P2 | 模板、Job、产物、指标 | Admin API、持久化与指标查询能力 |

M1–M3、M4 的只读部分与 M5 的 C25/C26（含持久化历史与产物预览）已完成，C27 已随根 STATUS 的 P08 完成、C28 已随根 STATUS 的 P09a 完成、C29（告警）已随根 STATUS 的 P09b 完成（见各节勾选项与验收记录），C24 的路由管理写路径已交付，见下方 P02 验收。M4/M5 不阻塞首版管理闭环；没有数据来源的菜单暂不开放，不用示例数据冒充运行状态。

## M1：控制台基础与会话（P0）

- [x] **C01 页面框架**：替换欢迎页与默认元数据；以中文为首版界面语言，建立登录页和受保护的管理布局，提供导航、当前用户、角色、租户 ID 与退出入口。没有租户详情接口时不编造租户名称。
- [x] **C02 请求与会话边界**：采用浏览器 → Next.js 同源服务端入口 → ControlPlane Admin API 的方案。服务端固定上游地址并显式限定方法和路径，不做任意 URL 代理；浏览器无法指定上游 Host 或注入服务间凭据。
- [x] **C03 登录与会话生命周期**：登录令牌存入 HttpOnly Cookie，生产开启 Secure，设置合适的 SameSite 与到期时间；写操作校验来源并落实 CSRF 防护。明确刷新页面时的用户信息恢复方案，身份只能来自可信会话，不能信任浏览器可修改的角色值；鉴权最终由控制面执行。
- [x] **C04 退出与失效**：退出先撤销 ControlPlane Redis 会话，再清理 Cookie 和用户数据缓存；过期或受保护请求返回 `401` 时转到登录页；`503`/断网时保留 Cookie 供重试，只允许站内返回地址，不虚构自动续期能力。
- [x] **C05 统一 API 客户端**：依据 Go JSON 契约维护类型与响应校验；覆盖数组、可选字段、日期、`204`、`400/401/403/404/409/5xx`、断网与超时。请求可取消，读请求重试有上限，创建 Key、创建用户等写请求不自动重试。
- [x] **C06 共享交互**：统一表格、表单错误、加载、空数据、失败重试、确认对话框与通知。表单提交期间防重复点击；只读查询失败不呈现为“零条记录”。
- [x] **C07 数据与凭据保护**：认证响应、业务响应不进入共享缓存；令牌、密码、Key 明文不进入 localStorage、URL、日志、异常文本或分析事件。前端只采用必要字段和固定错误文案；涉及 Go 日志时遵守 `runtime.Redact` 约定。

**实现位置**

| 任务 | 落点 |
| --- | --- |
| C01 | `app/layout.tsx`（中文元数据、`robots: noindex`）、`app/login/`、`app/console/layout.tsx`、`app/console/console-shell.tsx`、`app/console/page.tsx`。M1 验收时导航只有「概览」；后续 M2–M5 已补业务菜单，租户展示以真实接口为准 |
| C02 | `app/api/session/route.ts`、`app/api/admin/[...path]/route.ts`、`lib/console/upstream-routes.ts`（方法 + 路径 + 查询参数白名单）、`lib/server/control-plane.ts`（上游地址来自配置） |
| C03 | `lib/console/session-payload.ts`（AES-256-GCM 密封，含 `SESSION_COOKIE`）、`lib/server/session.ts`（HttpOnly / SameSite=Lax / 生产 Secure / 到期跟随令牌）、`lib/console/request-origin.ts`（写操作同源校验）、`lib/server/config.ts` |
| C04 | `app/api/session/route.ts` 的 `DELETE`、`app/console/console-shell.tsx`（退出后 `router.refresh()` 清路由缓存）、`components/console/use-console-request.ts`（401 转登录）、`lib/console/internal-path.ts`（只允许站内返回地址）、`proxy.ts` |
| C05 | `lib/console/api-client.ts`、`lib/console/contract.ts`、`lib/console/errors.ts` |
| C06 | `components/console/states.tsx`、`submit-button.tsx`、`confirm-dialog.tsx`、`data-table.tsx`，通知用已装的 sonner（`Toaster` 挂在根布局） |
| C07 | `lib/server/responses.ts`（全部响应 `no-store`）、`app/api/admin/[...path]/route.ts`（错误体不透传上游文案）、`lib/console/errors.ts`（固定中文文案）。会话只存 Cookie，不进 localStorage；令牌与密码不出现在任何浏览器可读处 |

**验收**：登录后刷新仍能恢复可信身份；未登录直接访问管理页会跳转；会话到期与退出后不显示旧租户数据；连续切换两个租户的账号不会串数据；服务端转发拒绝非允许路径及伪造来源的写请求。

**验收结果（2026-09-06，桩控制面）**：门禁 `pnpm lint`、`pnpm typecheck`、`pnpm test`（29 用例）、`pnpm build` 全部通过。以 `next start` 对接一个实现登录与 `GET /admin/v1/users`、`GET /admin/v1/audit` 的本地桩控制面，逐条核对：

- 未登录访问 `/console/keys` → `307` 到 `/login?next=%2Fconsole%2Fkeys`；伪造 Cookie 访问 `/console` → `307` 到 `/login`（proxy 放行、布局拒绝）。
- 登录无 `Origin` 或跨源 `Origin` → `403`；密码错误 → `401`；成功登录的 `Set-Cookie` 为 `HttpOnly; SameSite=lax`（生产 `Secure`），Cookie 值与页面 HTML 中均不含上游令牌。
- 带会话读用户 → `200`；无会话、退出后、篡改 Cookie → 均 `401`。
- 白名单外的 `POST /admin/v1/auth/login`、`POST /internal/v1/apikeys/verify`、`DELETE /admin/v1/users` → 均 `404`；写操作跨源 → `403`；`?limit=5&tenant_id=other` 只透传 `limit`；上游 `404` 转为 `{"error":"upstream_error"}`，不含上游文案。
- 两个租户账号先后登录：各自页面只出现自己的租户 ID、姓名与角色，严格边界匹配另一租户的 `t-1`/`u-1`/邮箱均为 0 次。

未覆盖：真实控制面（PostgreSQL + JWT）的联调属于 Q05/M2；组件级 DOM 测试属于 Q03 人工验收。

## M2：现有 Admin API 的管理闭环（P0）

### 用户

- [x] **C08 用户列表**：展示名称、邮箱、角色、状态、最近登录与创建时间；当前接口是全量数组，本地筛选和排序明确只作用于已加载数据。
- [x] **C09 创建用户**：仅 owner 显示创建入口，字段使用 `email/password/name/role`；角色值对齐 `owner/admin/member`，默认选择 member。初始密码不限制长度、字符或非空，创建后清空密码并刷新列表。
- [x] **C10 用户权限反馈**：admin/member 只读；处理邮箱冲突和创建失败；没有后端接口的编辑、删除、禁用、重置密码操作不开放。

**验收**：owner 创建用户后，新用户可以登录且属于同一租户；admin/member 无创建入口，绕过页面直接提交仍被后端拒绝；错误不泄露密码。

### API Key

- [x] **C11 Key 列表**：展示名称、后端返回的 `display`、创建者、状态、过期时间、最近使用与创建时间；缺失时间显示为未知或无记录，不伪造数据；状态展示兼顾吊销与过期时间。
- [x] **C12 创建与一次性展示**：owner/admin 可创建；发送 `name` 与 `ttl_seconds`，支持默认 90 天、最长 365 天的有效期和显式“永不过期”。成功后仅在一次性对话框展示、复制明文，关闭或离开即清除；列表、通知与缓存中只保留展示形式。
- [x] **C13 吊销**：owner/admin 可吊销本租户 Key；member 仅对 `created_by` 等于当前用户 ID 的 Key 展示吊销操作。确认框显示名称与 `display`，成功处理 `204` 并刷新状态；请求结果不确定时先查列表，不盲目重发写请求。
- [x] **C14 生效提示**：说明吊销后 Gateway 可能在配置的 Key 缓存 TTL 内继续接受请求，默认 TTL 为 30 秒；不承诺所有部署固定 30 秒或立即失效。

**验收**：明文只在创建成功当次可见，关闭后无法从页面状态、缓存或日志找回；member 不能创建或吊销他人 Key；跨租户 Key ID 与不存在的 ID 都按 `404` 处理。

### 审计

- [x] **C15 近期审计列表**：按现有 `limit` 接口读取有限数量记录，展示时间、操作者 ID、动作、目标、详情与 IP；界面明确“最近 N 条”，不显示未经后端提供的总数或全量结论。
- [x] **C16 大列表渲染**：使用 TanStack Table + TanStack Virtual；本地筛选注明范围，长详情按文本安全展示。虚拟滚动只解决 DOM 数量，不能替代 M3 的后端分页和内存上限。
- [x] **C17 审计边界**：保持当前后端的租户与角色可见性；说明这是管理操作审计，不是推理请求日志，且当前后端审计写入不与业务操作原子提交。

**验收**：真实创建用户、创建/吊销 Key 后可查询相应记录；覆盖空列表、读取失败、长详情与未知动作类型；界面不声称具有完整的历史分页或全量检索。

## M2 实现位置与验收结果

| 任务 | 落点 |
| --- | --- |
| C08–C10 | `app/console/users/page.tsx`（服务端取角色）、`users-view.tsx`（列表与本地筛选）、`create-user-dialog.tsx`（仅 owner 可见，密码原样提交） |
| C11–C14 | `app/console/keys/page.tsx`、`keys-view.tsx`（状态列由 `apiKeyState` 计算，吊销确认含网关缓存说明）、`create-key-dialog.tsx`（TTL 选项与一次性明文对话框） |
| C15–C17 | `app/console/audit/page.tsx`、`audit-view.tsx`（TanStack Table + Virtual，条数选择器，范围说明与非原子写入说明） |
| 共享 | `lib/console/permissions.ts`（角色矩阵，单一来源）、`lib/console/format.ts`（时间、Key 状态、TTL、本地筛选）、`components/console/use-resource.ts`（加载/空/失败三态，失败不退化为空数组） |

**验收结果（2026-09-06）**：门禁 `pnpm lint`、`pnpm typecheck`、`pnpm test`（38 用例）、`pnpm build` 全部通过。随后以 **真实控制面 + 真实 PostgreSQL 17**（Docker，Redis 关闭）而非 Mock 联调，两个租户（Acme、Globex）、三种角色：

- 角色矩阵：创建用户 owner `201` / admin `403` / member `403`；创建 Key owner `201` / admin `201` / member `403`；`ttl_seconds` 超过 365 天 `400`；邮箱冲突 `409`；密码过短 `400`。
- 一次性 Key：创建响应含 48 字符明文，`display` 是它的 13 字符前缀；`ttl_seconds: -1` 的 Key 响应中 **不含** `expires_at` 字段（`omitempty`），界面显示「永不过期」。
- 吊销：member 吊销他人 Key `404`、不存在的 id `404`、admin 吊销 owner 创建的 Key `204`；**重复吊销返回 `404` 而非 `204`**——控制面按 active 行匹配，吊销不幂等。界面据此在 `404` 时刷新列表并说明「可能已被他人吊销」，不谎称成功。
- 跨租户：Globex owner 吊销 Acme 的 Key `404`，用户列表只见自己，Key 列表为空。
- 审计：登录、创建用户、创建与吊销 Key 均产生记录；详情用 `display` 指代 Key，**全量审计中检索明文出现 0 次**；member 同样可读（与控制面当前规则一致）。
- 浏览器（无头 Chrome 152 + CDP）：审计 200 条只渲染 12 行 DOM、虚拟容器高 480px；筛选 `apikey.revoke` 后剩 1 行；创建 Key 后弹出一次性明文，关闭后明文不在 DOM、不在 `localStorage`/`sessionStorage`（存储项为 0），列表只留 `display`；吊销确认框显示名称、`display` 与 30 秒缓存说明，吊销后行变「已吊销」且不再显示吊销按钮；会话在页面打开后失效时，下一次读取的 `401` 使页面转到 `/login?next=%2Fconsole%2Faudit`。

未覆盖：Redis 缓存下的吊销生效窗口（本次以无 Redis 配置联调）、Gateway 侧的实际拒绝时刻，两者属于数据面验证。

## M3：配额与数据规模（P1，含后端依赖）

- [x] **B01 当前租户读取接口（ControlPlane）**：提供会话保护的当前租户资料及配额读取能力，明确角色范围、字段与错误契约；接口路径在后端设计时确定。不得借用 InternalToken 校验接口读取配额。
- [x] **C18 配额表单（依赖 B01）**：先读取再编辑 `requests_per_minute`、`tokens_per_minute`、`max_concurrent`，仅 owner/admin 可提交；完整提交三项非负整数。零明确显示“不限制”，读取失败不以零值代替已有配置，空输入不隐式转为零。
- [x] **C19 配额保存与回读（依赖 B01）**：保存成功展示服务端结果，刷新页面重新读取。写响应中受 `omitempty` 影响省略的字段按配额契约解释为零；提交前提示整组覆盖，说明多 Key 共用租户额度，并核实缓存对实际生效时间的影响。
- [x] **B02 列表分页与筛选（ControlPlane）**：为用户、Key、审计明确分页、稳定排序、单页上限、筛选范围及响应结构；审计需支持时间范围、动作和操作者查询，覆盖租户隔离测试。
- [x] **C20 分页接入（依赖 B02）**：筛选条件同步到 URL，仅保存非敏感字段；取消过期请求，限制缓存页数与条数；审计长列表保留虚拟滚动，避免无限累积到浏览器内存。
- [x] **B03 权限与文档对齐（ControlPlane/仓库文档）**：处理前述配额状态、member Key 权限、用户删除与 `403/404` 描述差异；若产品希望 member 创建自己的 Key，单独变更后端授权并添加测试，不由前端自行放开。

**验收**：配额读写与刷新保持一致；未知配置不会被误设为无限制；跨租户请求被隔离；分页不漏页、不重复，筛选范围清晰；缓存与 DOM 数量均有边界。

## M3 实现位置与验收结果

### 后端（控制面）

| 任务 | 落点 |
| --- | --- |
| B01 | `GET /admin/v1/tenants/current`：`internal/handler/routes.go`、`handlers.go` 的 `currentTenant`、`internal/logic/service.go` 的 `CurrentTenant`、`internal/types/types.go` 的 `TenantProfileResponse`。读权限开放给全部登录角色、写仍限 owner/admin，理由写在 `CurrentTenant` 的注释里 |
| B02 | `internal/store/store.go` 新增 `Page[T]`、`ListQuery`、三个 Filter、keyset 游标编解码与 `GetAPIKey`；`memstore` 与 `gormstore` 各自实现；`internal/logic` 三个 List 方法改签名并校验时间窗与角色；`internal/handler` 解析查询参数；`types` 新增三个信封类型 |
| B03 | 控制面 README 重写「角色能做什么」为逐操作表格、区分 `403` 与 `404`、写明吊销不幂等、新增「列表分页」一节；根 `AGENTS.md` 修正「配额未做」 |

**契约破坏性变更**：三个列表端点从裸数组改为 `{items, next_cursor}` 信封。理由与取舍写在 `types.UserListResponse` 的注释里——裸数组没有地方放游标，而并列增设分页端点会留下两种读取同一份列表的方式。Console 是唯一消费方，随本次改动一并更新。

### 前端（Console）

| 任务 | 落点 |
| --- | --- |
| C18/C19 | `app/console/quota/`（服务端取角色 → `QuotaView` 先读后编辑 → `QuotaEditor` 整组提交）、`lib/console/format.ts` 的 `QUOTA_DIMENSIONS`/`formatLimit`/`limitProblem` |
| C20 | `lib/console/paging.ts`（纯逻辑：游标历史、上界、查询构造）、`components/console/use-paged-resource.ts`、`pager.tsx`、`use-url-filters.ts`（URL 同步 + 防抖）；三张列表视图改为服务端筛选与翻页 |

**验收结果（2026-09-06，真实控制面 + PostgreSQL 17 + 无头 Chrome 152）**：Go 侧 `gofmt`/`go vet`/`go build`/`go generate` 一致性/`go test ./...`/`go test -race ./service/...` 全部通过；Console 侧 `pnpm lint`/`typecheck`/`test`（47 用例）/`build` 全部通过。

- **B01**：未配置配额的租户读回 `"limits":{}`（三项 `omitempty` 全省略），页面显示「不限制」而不是空白；写入 600/90000/8 后回读一致；三项置零后写响应为 `{}`、读响应为 `{"limits":{}}`。member 读 `200`、写 `403`，负数 `400`。
- **B02**：造 60 个 Key，以 `limit=7` 翻 9 页共取到 60 条、唯一 60 条，不重不漏；`limit=10000` 实际返回 200 条（上限截断）、`limit=abc` 返回 50 条（默认值）；无效游标、颠倒时间窗、畸形时间、未知角色筛选均 `400`；`q`、`action`、时间窗筛选结果均正确，2020 年的窗口返回 0 条。
- **C18/C19（浏览器）**：零值渲染为输入框 `0` 且旁注「不限制」；清空输入框提交被拒绝并提示「请填写数值」，不会被当作 0；保存后就地显示服务端返回值（90,000 带千分位），刷新页面仍是 600。
- **C20（浏览器）**：第一页 50 行、上一页禁用；下一页得到 10 行并标注「已到末页」，与第一页零重复；回退准确落回第 1 页。逐字符快速输入 `k→key-007` 后只呈现最终查询的 1 条结果（**分页/筛选响应乱序**的验证），URL 同步为 `?q=key-007`、**不含游标**，筛选后回到第 1 页；带 `?q=key-007` 直接打开，筛选框与结果都从 URL 恢复。

未覆盖：gorm 实现的 keyset SQL 只在 PostgreSQL 上跑过，MySQL 未实测；`likePattern` 的转义假定默认转义符 `\`，MySQL 开启 `NO_BACKSLASH_ESCAPES` 时行为不同。两者记为已知限制。

## M4：节点、运行时、模型与路由（只读已完成，管理写路径待开发）

| 任务 | Console 交付 | 必须先具备的后端能力 |
| --- | --- | --- |
| [x] C21 节点与运行时 | 节点列表、在线状态、最近心跳、标签、运行时与能力详情；缺失指标显示不可用 | ControlPlane 聚合节点快照、权限过滤、数据时间戳；CPU/GPU 等字段须有真实上报来源 |
| [x] C22 节点管理 | 接入说明、审批、禁用、删除、维护状态与操作审计 | 正式管理接口与生效路径；不能用 Console 操作直接替代 Registry 内部能力 |
| [x] C23 模型与部署目录 | 运维只读的模型、节点/runtime 与能力目录 | ControlPlane Operator API 聚合 Gateway 快照；不是租户模型授权或持久化 Deployment 管理 |
| [x] C24 路由配置 | 编辑真实模型映射、节点选择器、优先级与权重，校验并展示生效结果 | 路由持久化、版本、发布/回滚、Gateway 同步与审计；当前文件配置不等于管理 API |

**验收**：页面数据均来自授权的 Admin API；过期快照有明确提示；写操作能确认实际生效状态，失败不会伪装为已发布。

### M4 的两个前置决定

开工前有两件事在 STATUS 里没有答案，且会改变要做的东西，因此先定下来：

1. **节点没有租户维度。** `node_id` 只来自证书，任何租户的请求都可能被路由到任何节点，因此 C21 要求的「权限过滤」没有可过滤的字段。选定的方案是**平台运维视图**：机群清单不挂在会话守卫的 Admin API 上，而是自己的路径前缀 `/operator/v1/*` 加自己的密钥；租户角色无论怎么调整都够不到。
2. **本阶段只做只读闭环**（C21 + C23）。C22（节点审批/禁用/维护）与 C24（路由持久化、版本、发布回滚、Gateway 同步）各自需要新建写路径与跨服务下发设计，属于独立工程，未做。

### C21 / C23 实现位置

| 层 | 落点 |
| --- | --- |
| 契约 | `common/nodeview/`：Gateway 与控制面共用的机群清单形状。渲染是允许列表——只输出点名的字段，不序列化 `NodeInfo` 或 `Descriptor` |
| Gateway | `adminapi/`：`-admin-addr` 上的 `GET /internal/v1/nodes`，Bearer token 来自 `AISW_GATEWAY_ADMIN_TOKEN`，无 token 拒绝启动，只读 |
| 控制面 | `internal/fleet/`：并发询问全部副本并合并（同一节点只出现一次，取「认为它在线」且心跳更新的那份视图）；`internal/config` 的 `FleetConf`；`GET /operator/v1/nodes`、`/operator/v1/models`，只在配置时挂载 |
| Console | `app/api/operator/`（第二个转发入口，用部署密钥）、`lib/server/operator.ts`（谁可用）、`lib/console/fleet.ts`（契约校验）、`app/console/fleet`、`app/console/models`、`components/console/fleet-freshness.tsx` |

**验收结果（2026-09-06，真实 Gateway + 真实控制面 + PostgreSQL 17 + 无头 Chrome 152）**：Go 侧 `gofmt`/`vet`/`build`/`generate` 一致性/`go test ./...`/`-race` 全通过；Console 侧 `lint`/`typecheck`/`test`（54 用例）/`build` 全通过。

联调拓扑刻意是三个副本：一个**真实 Gateway**（开 `-admin-addr`，无 Agent 连接因而节点为空）、一个按 `nodeview` 契约作答的替身副本（两个节点、三个运行时），以及一个**死地址**用来验证局部失败。

- **Gateway 端点**：无 token `401`；带 token 返回 `{"replica_id":"…","generated_at":"…","nodes":[]}`——空机群是空列表加时间戳，不是缺失字段。
- **控制面聚合**：无 operator token `401`；**拿 Gateway 的 token 冒充也 `401`**（两个密钥授权方向相反，不可互换）；聚合结果 `partial: true`，三个副本分别报告 `0 节点` / `2 节点` / `unreachable`，节点合并且带上报告它的副本。
- **Console 授权**：名单外的租户 admin 访问 `/console/fleet`、`/console/models`、`/api/operator/*` 一律 `404`（不是 403——「不能看」与「这里没有」必须长得一样），导航里也没有这两项；名单内的运维 `200` 且导航出现。运维入口只接受 GET（`POST` → `405`），白名单外的运维路径 `404`，页面 HTML 中不含运维 token。
- **浏览器渲染（22 项全过）**：两个节点都渲染，在线/离线/退出中分别标出，Agent 自述标签、不健康运行时、已脱敏诊断、降级说明都如实展示；从未做过健康检查的运行时显示「未完成过健康检查」而不是 0ms；失联副本以固定文案「连不上」呈现，页面顶部有「清单不完整」提示。模型页把同一模型的多处部署合并为一条，`qwen-7b` 显示「可用部署 1 / 2」——离线节点上的那处被列出但不计入可用；页面明说这些不是调用方可用的名字。

**过程中修掉的一个真实缺陷**：运维页面最初复用了统一请求层的默认入口 `/api/admin`，被租户白名单挡成 `404`，在浏览器里表现为「机群为空」。修法是给请求加 `surface` 字段显式选择入口，并补了一条会抓住它的用例。默认值仍是 `admin`——在要紧的方向上是安全的：忘记写只会 404，反过来做默认值则会悄悄花掉运维 token。

**已知缺口（记入控制面 README 的「已知缺口」6、7 两条）**：系统里没有平台运维身份，机群端点背后没有用户，因此控制面无法记录是谁读的，也无法把权限授予某个具体的人——当前由 Console 侧名单决定，这是缺口不是设计。建议运维控制台与面向租户的控制台分开部署。

## M5：工作流、Job 与可观测性（目录与 Job 页面已接入，指标等待开发）

| 任务 | Console 交付 | 必须先具备的后端能力 |
| --- | --- | --- |
| [x] C25 工作流模板（只读范围） | 模板目录、输入声明、校验状态与运维副本一致性 | Gateway 文件目录经控制面聚合；版本管理、依赖编辑与发布留给根 STATUS P03 |
| [x] C26 Job 与产物（当前范围） | 实时列表、持久化历史与详情、状态快照、取消、产物预览/下载 | 历史走 ControlPlane；取消与产物走 Console 服务端 Gateway 例外路径；尚未接入 Console SSE，重启后的下载受 Gateway 映射恢复限制 |
| [x] C27 总览与指标 | `/operator/metrics`：请求量（按端点/状态）、成功率、延迟 p95 近似值、Token 用量与容量五组 ECharts 曲线，`dataZoom` 可交互；空态与错误态分离 | 控制面 `GET /operator/v1/metrics/history`（P08）：`internal/metricshistory` 定时抓取 Gateway/Registry 的 `/metrics` 文本、按标签白名单聚合后落地到关系库的 `metrics_history_points` 表；是平台运维视角，不按租户拆分——现有 Gateway 指标按设计不带 `tenant_id` 标签 |
| [x] C28 请求与错误检索 | `/console/audit?tab=requests`（审计页的第二个 Tab，旧 `/console/requests` 重定向至此）、`/operator/requests`：按时间、状态、request ID 查询脱敏元数据，共享 `app/console/requests/request-log-view.tsx` 列表/分页/筛选视图；运维视角额外多一个 `tenant_id` 筛选框 | 控制面 `POST /internal/v1/requestlogs`（Gateway 异步批量上报）、`GET /admin/v1/requests`、`GET /operator/v1/requests`（P09）：新表 `request_logs`，保留期默认 30 天；不展示或记录完整 Prompt、鉴权头 |
| [x] C29 告警 | `/operator/alert-rules`（规则列表 + 创建/编辑/启停表单，`app/operator/alert-rules/alert-rules-view.tsx`）、`/operator/alerts`（实例列表，展示状态/触发时间/确认时间/通知发送状态，`firing` 实例提供"确认处理"按钮，`app/operator/alerts/alerts-view.tsx`），均挂在既有 `/operator` 导航、与 `/operator/metrics`（C27）相邻 | 控制面 5 个规则 CRUD 端点（`/operator/v1/alert-rules`）与 2 个实例端点（`GET /operator/v1/alerts`、`POST /operator/v1/alerts/:id/acknowledge`），均 `requirePlatformSession`（P09）：新表 `alert_rules`/`alert_instances`，评估循环无条件运行、默认 60 秒一轮；告警判定在服务端计算，浏览器只做展示与手动确认 |

Job 事件逐条消费，页面离开时取消订阅，断线恢复依赖明确的后端契约；历史事件数量有上限。产物采用授权流式下载或短期链接，避免整文件缓冲。Gateway 实时视图来自有界内存；已落库的历史记录由控制面查询。非终态恢复与产物下载边界见 [Gateway STATUS](../aiServeWeaveGateway/STATUS.md#持久化与访问的现有限制)。

### M5 的两个前置决定

1. **M5 初次实施范围为 C25 + 实时 Job 视图。** 当时没有持久化历史；后续 J03–J08/C26 已补 MySQL 历史、详情与产物页面。当前仍缺指标时序、请求检索与告警所需的数据源。
2. **模板两边都开。** 租户看到菜单（能提交什么、接受什么输入），运维额外看到发布状态（哪些副本注册了它、是否一致）。无论哪一侧，完整的 ComfyUI 图都不输出。

### C25 / C26（实时部分）实现位置

| 层 | 落点 |
| --- | --- |
| 契约 | `common/workflowview/`：模板目录与 job 页。模板不含图、也不含输入所写入的节点与字段；job 不含节点 id、运行时 id 与解析后的模型 |
| Gateway | `httpapi.Server` 暴露 `JobsFor(tenantID)` 与 `Templates()` 两个只读窗口；`adminapi` 新增 `GET /internal/v1/workflows` 与 `GET /internal/v1/jobs?tenant_id=`（tenant_id 必填）；`jobStore` 新增 `forTenant` 与逐出标志 |
| 控制面 | `internal/fleet/workflows.go`（目录合并与不一致判定）、`jobs.go`（按租户聚合）；`/admin/v1/workflows`、`/admin/v1/jobs`（会话）与 `/operator/v1/workflows`（运维），租户响应清空副本身份 |
| Console | `lib/console/fleet.ts` 的两个解析器、`app/console/workflows`（菜单 + 运维区块）、`app/console/jobs`（实时视图） |

**验收结果（2026-09-06，两个真实 Gateway + 真实控制面 + PostgreSQL 17 + 无头 Chrome 152）**：Go 侧 `gofmt`/`vet`/`build`/`generate` 一致性/`go test ./...`/`-race` 全通过；Console 侧 `lint`/`typecheck`/`test`（61 用例）/`build` 全通过。

拓扑刻意模拟一次没做完的发布：副本 A 加载两个真实模板，副本 B 只加载 `portrait` 且描述不同，再加一个按契约作答的 job 桩副本。

- **Gateway**：真实文件配置加载的模板经端点返回后，`class_type`、`KSampler`、`graph`、`node`、`field` 出现次数均为 **0**；job 端点无 `tenant_id` 返回 `400`，有则返回该租户的页。
- **控制面**：租户菜单与运维目录来自同一次读取；运维看到 `portrait` 在 replica-a/b 且 `divergent`（描述不同）、`upscale` 只在 replica-a 且 `divergent`，未提供该端点的第三个副本报 `unsupported` 并置 `partial`。四种凭据交叉测试：会话取运维目录 `401`、运维 token 取租户菜单 `401`、Gateway token 与 InternalToken 取运维目录均 `401`。
- **租户隔离**：租户响应中 `replica-a`、内部地址、`endpoint` 出现次数均为 **0**，`partial`/`truncated` 保留；job 字段只剩 `created_at,id,queue_position,state,updated_at,workflow_id`。e2e 测试守住这条线。
- **浏览器（租户 18 项 + 运维 6 项全过）**：工作流页渲染两个模板与输入约束，图不出现，租户看不到副本名与内部端口，不一致以租户措辞提示并解释后果；运行页三条运行齐全，顶部明说「实时视图不是历史」，表被截断时另有提示，无「所在副本」列，产物只给数量。运维视角额外出现「副本一致性」区块，逐模板列出注册它的副本。

**过程中修掉的两个缺陷**：其一，租户的工作流与运行响应最初携带副本 id 与内部 endpoint（`http://127.0.0.1:8091`）——内部网络主机名泄漏到租户浏览器，与 M4 把节点定为运维视图的判断相冲突，已在控制面返回前清空并补了 e2e 断言。其二，后端报告了排队位置，界面却因为状态不是 `pending` 而丢掉它——为了让一个猜测显得整齐而藏起一个事实，已改为只要报告了就展示。

**取消与产物下载已实现，走的是声明过的例外。** 见 Console `AGENTS.md`「与后端的边界」一节：本控制台服务端持有一把该租户的 Gateway API Key（owner/admin 在「设置」页粘贴，由本控制台加密存储在会话 cookie 里，从不下发到浏览器），未做 scope 收紧，取消（`app/api/gateway/jobs/[id]/cancel`）与产物下载（`app/api/gateway/artifacts/[id]`，边读边送、不缓冲）经由 `lib/server/gateway.ts` 直连 Gateway 数据面，授权判断（谁能配置这把 key）由 `lib/console/permissions.ts` 的 `canManageGatewayKey` 在服务端自己执行——这是本代码库里少数几个没有下游再检查一遍、本身就是唯一防线的权限判断。已知代价是这把 Key 权限与用户自建 Key 相同，被拿到后不只能取消/读产物、也能拿去跑推理烧配额——这是当前没有真实租户在生产环境运行阶段的刻意权衡，不是遗漏，有真实租户后应重新评估是否收紧。**同时接在 C25/C26 的实时视图与持久化历史详情页上。**

**持久化历史列表页、Job 详情页与产物预览均已实现。** 后端 `GET /admin/v1/jobs/history` 与 `GET /admin/v1/jobs/history/:id`（见根 STATUS 的 J07）已由 `app/console/jobs/history`（列表，`lib/console/contract.ts` 的 `parseJobHistoryEntries`）与 `app/console/jobs/history/[id]`（详情，`parseJobHistoryDetail`）接上；两个新端点加进了 `lib/console/upstream-routes.ts` 的转发白名单。详情页对图片/视频扩展名内联预览（`<img>`/`<video>` 直接指向 `app/api/gateway/artifacts/[id]`），其余产物退回下载链接——这与实时视图共用同一个下载路由，区别只是多了一层按扩展名的展示选择，猜不出媒体类型时不强行渲染。产物 id 能出现在详情页，前提是根仓库这一轮同时补上的一处后端缺口：Gateway 此前从未把 `listArtifacts` 铸造的产物 id 上报给控制面的 `job_artifacts` 表，`jobPersister` 现已新增 `persistArtifacts` 补上这条旁路（见 ControlPlane README「Job 持久化契约」）。C27 已随根 STATUS 的 P08 完成，见下方「C27（指标）实现位置」；C28（请求与错误检索）已随根 STATUS 的 P09a 完成，见下方「C28（请求检索）实现位置」；C29（告警）已随根 STATUS 的 P09b 完成，见下方「C29（告警）实现位置」。

**验收**：事件流可取消且内存有界；产物受租户授权保护；C27 图表区分「加载失败」（`ErrorState`，带重试）与「无数据」（`EmptyState`），显示查询窗口；完整工作流与 Prompt 不进入日志、异常或遥测。

**已知简化**：C27 的「无数据」态目前不再进一步区分"该窗口确实无流量"与"采集器尚未运行到/数据已过保留期"——Prometheus 计数器对从未出现过的标签组合(如从未发生过的某个 `status` 值)本就不产生序列，这两种情形在查询侧天然不可分辨，除非另加一个独立的"存活"信号；当前 `EmptyState` 的文案把两种可能都说明，而不是伪造一个无法可靠计算的区分。

### C27（指标）实现位置

| 层 | 落点 |
| --- | --- |
| 契约 | `internal/types.MetricsHistoryResponse`（控制面）、`lib/console/metrics.ts` 的 `parseMetricsHistory`（Console） |
| 控制面 | `internal/metricshistory`（采集器 + 保留期清理）、`internal/store` 的 `MetricsHistory` 接口（gormstore/memstore 两个实现）、`internal/logic.ListMetricsHistory`、`internal/handler/metricshistory.go` 的 `GET /operator/v1/metrics/history` |
| Console | `lib/console/metrics-charts.ts`（`deltaByBucket`/`approxP95` 纯逻辑）、`app/operator/(protected)/metrics`（`/operator/metrics` 页面与视图） |

后端指标接入（Registry、控制面自身的 HTTP 请求量/耗时、吊销 outbox 滞后量、Fleet/Registry 客户端调用结果）与跨服务 trace（`common/reqid` 把 request_id 从 Gateway 前门带到 Scheduler/Tunnel/Agent 的结构化日志）不在 Console 范围内，实现位置见根 STATUS 的 P08 行与各服务 README。

### C28（请求检索）实现位置

| 层 | 落点 |
| --- | --- |
| 契约 | `internal/types.RequestLogResponse`/`RequestLogListResponse`（控制面）、`lib/console/contract.ts` 的 `RequestLogEntry`/`parseRequestLogEntries`（Console） |
| Gateway | `httpapi/requestlog.go`（中间件、状态码→outcome 封闭映射）、`httpapi/requestlogpush.go`（有界 channel + 后台批量推送器）、`controlplaneclient` 的 `RequestLogsClient` |
| 控制面 | 新表 `request_logs`（`internal/store/gormstore/requestlogmigrate.go` 固定版本迁移）、`internal/store` 的 `RequestLogs` 接口（gormstore/memstore 两个实现）、`internal/logic.CreateRequestLogs`/`ListRequestLogs`、`internal/handler` 的 `POST /internal/v1/requestlogs`、`GET /admin/v1/requests`、`GET /operator/v1/requests`、`internal/requestlogretention`（保留期清理，默认 30 天） |
| Console | `app/console/requests/request-log-view.tsx`（租户/运维共享的列表/分页/筛选视图）、`app/console/audit/audit-tabs.tsx`（租户审计页的「请求检索」Tab；`app/console/requests/page.tsx` 仅重定向）、`app/operator/(protected)/requests/page.tsx`（`/operator/requests`，多一个 `tenant_id` 筛选框）、`lib/console/upstream-routes.ts` 新增两条转发白名单 |

详见根 STATUS 的 P09 行、[Gateway STATUS「请求日志中间件与推送（P09/C28）」](../aiServeWeaveGateway/STATUS.md#请求日志中间件与推送p09c28)、[ControlPlane STATUS「请求日志检索（P09/C28）」](../aiServeWeaveControlPlane/STATUS.md#请求日志检索p09c28)。Go 全量质量门禁与 Console `lint`/`typecheck`/`test`（149 用例）/`build` 通过；未接入真实 PostgreSQL/MySQL 跑 live 测试，详见根 STATUS 的 P09 行。

### C29（告警）实现位置

| 层 | 落点 |
| --- | --- |
| 契约 | `internal/types.AlertRuleResponse`/`AlertInstanceResponse` 等（控制面）、`lib/console/contract.ts` 的 `parseAlertRules`/`parseAlertInstances`（Console） |
| 控制面 | 新表 `alert_rules`/`alert_instances`（`internal/store/gormstore/alertingmigrate.go` 固定版本迁移）、`internal/alertengine`（评估循环 `evaluator.go`、派生指标算法 `metrics.go`、Webhook 发送 `webhook.go`）、`internal/store` 的对应接口（gormstore/memstore 两个实现）、`internal/logic/alertrules.go`/`alertinstances.go`、`internal/handler` 的 5 个规则 CRUD 端点与 2 个实例端点（均在 `/operator/v1/alert-rules`、`/operator/v1/alerts` 下） |
| Console | `app/operator/alert-rules/alert-rules-view.tsx` + `app/operator/(protected)/alert-rules/page.tsx`（`/operator/alert-rules`）、`app/operator/alerts/alerts-view.tsx` + `app/operator/(protected)/alerts/page.tsx`（`/operator/alerts`）、`lib/console/upstream-routes.ts` 新增 7 条转发白名单 |

详见根 STATUS 的 P09 行与 [ControlPlane STATUS「告警评估与通知（P09/C29）」](../aiServeWeaveControlPlane/STATUS.md#告警评估与通知p09c29)。Go 全量质量门禁与 Console `lint`/`typecheck`/`test`（157 用例）/`build` 通过；未接入真实 PostgreSQL/MySQL 跑 live 测试，也未做需要真实平台会话与真实 Webhook 网关的完整交互式浏览器验收，详见根 STATUS 的 P09 行。

### 工作流模板的节点图预览（P03 后续）

`/operator/workflow-templates` 在图 JSON 文本框下方实时渲染只读节点图，历史比较区可在「节点图 / JSON」之间切换，两侧并排并标出差异节点（黄框：两侧都有但内容不同；蓝框：只在一侧）。图的唯一编辑入口仍是文本框，本阶段**不做**画布编辑。

| 层 | 落点 |
| --- | --- |
| 纯逻辑 | `lib/console/comfy-graph.ts`：API Format → 节点/连线、左到右最长路径分层布局（自写，不引布局库）、模板输入/输出绑定叠加、按排序键比较的节点差异；格式问题（缺 `class_type`/`inputs`、悬空引用、环、绑定未命中）记为问题列表而不抛异常；超过 `MAX_PREVIEW_NODES`（500）直接拒绝预览 |
| 画布 | `components/console/comfy-graph-preview.tsx`：`@xyflow/react` 渲染，只读（不可拖拽/连线），点击节点在下方查看完整取值 |
| 页面 | `app/operator/(protected)/workflow-templates/workflow-templates-view.tsx`：草稿区预览读 `useDeferredValue` 副本，避免大图在输入路径上重算 |

已知限制：API Format 不含坐标，布局是重建的，不等于作者在 ComfyUI 里的摆放；没有节点定义（`/object_info`），输出槽只能显示序号而不是 `IMAGE`/`LATENT` 这类名称，字段也不区分 widget 种类。可视化编辑见下一节。图只出现在运维页面，组件注释写明不得用于租户页面。

### 工作流模板的可视化编辑（P03 后续）

同一页面新增可折叠的「可视化编辑器」区：以 `wujie` 同源挂载官方 ComfyUI_frontend 1.55.17，后端是控制面的只编辑桩（见 [ControlPlane STATUS「工作流可视化编辑器」](../aiServeWeaveControlPlane/STATUS.md#工作流可视化编辑器p03-后续)）。打开时自动载入草稿（有一致的布局就载入布局，否则让编辑器排布 API 图）；「导出到草稿」把编辑器的 API 图写回文本框、把 UI 工作流作为布局写进草稿，发布时一并保存；「切换 App 模式」调用前端自己的 App 模式。布局变化即新版本。

| 层 | 落点 |
| --- | --- |
| 纯逻辑 | `lib/console/comfy-editor.ts`：编辑器路径、上游解析（逐段重编码，拒绝 `.`/`..`/控制字符，方法限 GET/HEAD/POST/DELETE）、子应用网络允许列表、导出校验（`parseEditorExport`，剥离画布视口 `extra.ds`）、假 WebSocket 脚本；`lib/console/workflow-templates.ts` 新增 `workflow` 字段、`MAX_WORKFLOW_BYTES`，`MAX_CONTENT_BYTES` 改为 8 MiB + 256 KiB |
| 转发 | `app/api/operator/comfy-editor/[...path]/route.ts` + `lib/server/control-plane.ts` 的 `streamControlPlane`（流式、只回传固定响应头、写请求体有界读取后丢弃） |
| 宿主 | `components/console/comfy-editor.tsx`：动态加载 wujie（每次挂载使用独立实例名，避免异步销毁误拆新实例），`load`/`exportGraph`/`toggleAppMode` 句柄；程序化载入后 1 秒内的变更事件不算作运维编辑 |
| 页面 | `workflow-templates-view.tsx`：布局只在与文本框图文本一致时随发布提交，手改 JSON 后提示布局失效；切换模板时关闭编辑器，避免把上一个模板的图导出进当前草稿；导出含目录外节点时拒绝并列出节点类型 |

验证：Node 测试新增 `lib/console/comfy-editor.test.ts`（上游解析、允许列表、导出校验、`workflow` 字段、假 WebSocket）；用真实控制面桩（Go）+ 真实前端发布包 + 本仓库 `bridge.js` + 真实节点目录在专用 Chrome 里端到端验证了 API 图载入、连线保留、文件下拉清空后已有取值保留、二次载入 UI 工作流后导出一致、目录外节点被列入 `missing`、外连（huggingface）被允许列表拦下。**未验证**：经 Next.js 转发路由与真实平台会话的整页交互（需要可登录的控制面与 Console 构建），以及 `pnpm build` 下 wujie 的动态导入。

已知限制：含自定义节点的模板只能继续用 JSON 编辑；模型/LoRA/图片等文件下拉为空（已有取值保留）；同源嵌入是已接受的风险（见 AGENTS「ComfyUI 编辑器」）；编辑器里的「运行」按钮会得到 403。

### API 文档（租户）实现位置

| 层 | 落点 |
| --- | --- |
| 契约 | 控制面 `internal/apidocs`（文档结构、校验规则、内置种子）与 `logic.APIDocSummary`/`APIDocDetail`/`OperatorAPIDoc*`；Console `lib/console/api-docs.ts`（解析、分组、占位符替换、写入体积上限） |
| 控制面 | 新表 `api_docs`（固定版本迁移）、启动时 `SyncAPIDocSeeds` 填充 19 份内置文档；租户 2 个只读端点、平台 6 个管理端点，见 [ControlPlane STATUS「API 参考文档（租户）」](../aiServeWeaveControlPlane/STATUS.md#api-参考文档租户) |
| Console | 租户 `/console/docs`（快速开始 + 端点索引）与 `/console/docs/[slug]`（左侧分组导航、参数表、多语言请求示例与按状态码的响应示例，`app/console/docs/`）；运维 `/operator/api-docs`（JSON 编辑 + 实时预览、恢复内置版本、删除）；渲染部件 `components/console/api-doc-article.tsx` 两侧共用；`lib/console/upstream-routes.ts` 新增 8 条转发白名单 |

示例中的基地址取 `AISW_CONSOLE_PUBLIC_GATEWAY_URL`（租户可访问的公开 Gateway 地址，可带路径前缀），未设置时退回 `AISW_CONSOLE_GATEWAY_URL`。

## 测试、文档与交付门禁

密码策略变更：已取消初始密码的长度、字符与非空限制，创建租户、创建用户和登录保持一致；
长密码完整参与校验，旧 bcrypt 账号继续兼容。已更新运行中的控制面与 Console，并在真实
部署验证短密码、空密码和长密码的创建与登录、错误密码拒绝以及原账号登录；临时测试账号
已清理。Console lint、类型检查、68 个测试、镜像构建，以及 Go vet/build/test/race 通过。
本机 `go generate ./api/...` 因 protoc 7.35.0 与仓库记录的 7.34.1 不同，只产生版本注释差异；
生成文件已恢复，未将无关变更纳入本次修改。

部署补充：`deploy/docker-compose.yaml` 已包含 `console` 服务，使用本目录的独立 Dockerfile
构建 Next.js standalone 镜像。会话密钥在运行时注入，本地入口默认 `127.0.0.1:3000`；
升级已有部署时需补充 `AISW_CONSOLE_SESSION_SECRET`。完整配置及 HTTPS 要求见
[部署说明](../../deploy/README.md)。

部署验证：Compose 配置解析、缺少密钥时拒绝启动、Console 镜像构建均通过；临时隔离容器
验证了非 root 运行、登录页、静态资源、HttpOnly Cookie 与认证请求转发。容器内使用测试
控制面，未启动现有 Compose 部署；Console 的 lint、类型检查与 69 个单元测试通过。

- [x] **Q01 测试基础（M1）**：选用 Node 26 内置的 `node:test` + `node:assert/strict`，**没有新增任何依赖**（理由与两条使用约束见 [AGENTS.md](AGENTS.md)）；脚本为 `pnpm test`，当前 47 个表驱动用例覆盖契约解析（含一次性 Key）、会话密封与过期、转发白名单、来源校验、请求层重试与错误分类、角色矩阵、Key 状态与密码创建与登录回环。`fetch` 与 `sleep` 通过参数注入，过期判定用注入的时间，不依赖真实网络、控制面或 `sleep`。
- [x] **Q02 契约与关键流程（随 M1–M3）**：覆盖登录失效、跨租户切换、角色矩阵、一次性 Key、`204`、失败重试、配额零值/缺省字段、分页响应乱序；测试数据与当前 Go 请求响应一致。
  - Console 47 个用例覆盖：登录失效与 401 转登录、跨租户隔离、角色矩阵、一次性 Key 与 `display` 前缀关系、`204`、读重试与写不重试、配额零值与 `omitempty` 缺省字段（`limits: {}`）、列表信封与 `next_cursor` 的四种取值、游标历史上界、查询参数构造、密码创建与登录回环、Key 过期与吊销状态。
  - 控制面新增 Go 用例覆盖：同一时刻写入行的全序分页（不重不漏）、翻页途中并发写入不影响后续页、单页上限与默认值、无效游标、三类筛选与租户隔离、颠倒/空时间窗、未知角色筛选、配额读写回环。
  - 「分页响应乱序」在浏览器中验证：逐字符快速输入筛选词时旧请求被取消，页面只呈现最终查询的结果（见 M3 验收记录）。

- [x] **Q03 界面验收（随各阶段）**：P10 已补 Chromium 验收；检查桌面与窄屏、键盘操作、表单标签、焦点恢复、错误可读性和长文本；大表格使用有上限的模拟数据检查滚动、筛选及内存增长。
  - 已在无头 Chrome（CDP 驱动）中核对：审计虚拟滚动只渲染可见行、本地筛选生效、创建 Key 对话框与一次性明文、关闭后明文离开 DOM 与浏览器存储、吊销确认框内容与吊销后状态、401 转登录并保留返回地址。表单每个输入都有 `label`，筛选框有 `aria-label`，错误用 `role="alert"`。
  - 2026-09-12（P10）：新增独立 `pnpm test:e2e`（Playwright + 生产构建），22 个已登录页面 × 1440/390/320px，另有两个登录页、键盘/错误/焦点/短屏/导航用例，共 75 项。审计与请求列表各连续翻阅 10,000 条合成记录（200 条/页），检查虚拟行上界、末页、键盘滚动与筛选；CDP GC 后比较第 5/25/50 页堆，增长门槛为 8 MiB。修复网格/表格撑宽、节点标签被卡片裁切、长模板按钮、短屏弹窗与缺失表单标签；路由页等待真实数据加载后才测量。覆盖范围是 Chrome 的这些页面和代表性交互，不代表所有浏览器、辅助技术或每个弹窗；API 使用隔离样本，不替代真实控制面鉴权联调。复现方法与实测见 [P10 验收手册](../../deploy/p10-acceptance.md)、[归档记录](../../docs/acceptance/p10-2026-09-12/README.md)。
- [x] **Q04 联调与使用文档（M2）**：将 Console README 改成实际启动、服务端配置、租户引导前置步骤、角色权限、测试和部署说明；新增环境变量时提供无密钥的示例，并区分服务端变量与可公开配置。
- [x] **Q05 首版交付（M2）**：真实 Admin API 完成“登录 → 创建用户 → 创建 Key → 关闭明文展示 → 吊销 → 查询审计 → 退出”；记录 owner/admin/member 和两个租户的验证结果。Mock 演示不算接口接入完成。

每次前端代码交付在 Console 目录执行：

```bash
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

TS 7 类型检查与使用 TS 6 的构建必须分别通过；`pnpm typecheck` 依赖 `.next/types`，新增路由后先跑一次 `pnpm build`。若同时修改 Go 后端，还需执行根目录 AGENTS 规定的全部 Go 质量检查与 proto 生成一致性检查。

纯规划文档修改核对文件路径、接口事实、依赖顺序与 Markdown 格式即可；不将未运行的构建或测试记为通过。后续勾选任务时补充实现位置及验收结果，后端依赖未完成的任务继续保持未完成。

## P01 阶段三 / C22 验收（2026-09-08）

已交付独立平台登录、平台会话类型与 Cookie、`/operator/*` 布局和四个页面、节点确认操作、Registry 待审批/离线状态及独立运维审计。租户页面不再承担平台访问，旧机群/模型入口跳转到运维面；旧部署 token 与邮箱名单代码和配置示例已移除。前面的 M4/M5 实施记录保留历史上下文，本节取代其中关于“无平台身份、共享密钥名单、C22 未做”的现状描述。

- 纯逻辑测试覆盖 Cookie 篡改/过期/互换拒绝、登录契约、平台写白名单、节点标签与无重试策略；控制面 e2e 覆盖平台/租户审计隔离、筛选与分页。
- Console lint、类型检查、Node 测试和 `pnpm build --webpack` 通过。默认 Turbopack 构建在本机沙箱中无法绑定编译工作端口；Webpack 使用原有配置完成生产构建，无新增依赖。
- Go 格式、vet、build、proto 生成一致性、全量测试与 service race 门禁通过（本地测试端口需在沙箱外运行）。未执行真实数据库/GPU/多副本传播验收。
- 本地隔离 HTTP 上游验证双会话同时使用、Cookie 属性与令牌不回传、独立退出、跨源拒绝、节点写操作、禁止访问引导接口和四页响应；浏览器验证平台登录、审批确认及刷新、审计入口、390px 窄屏，以及 Escape 关闭确认框后焦点回到操作按钮。
- 当前 Gateway 只提供合并维护观测，页面不承诺所有副本已生效；特殊路径分隔符节点 ID 的 HTTP 操作限制见 README。Q03 的全站大列表、所有错误状态及完整浏览器矩阵仍未验收，本轮不勾选 Q03。

## P02 / C24 验收（2026-09-08）

已实现 `/operator/routes`：结构化编辑、1 MiB 有界文件导入、校验、版本条件发布、历史分页与对比、回滚、逐副本生效查询。历史 M4 段落里的 C24“未做”指当时范围，由本节更新为已完成。

新增测试覆盖文档/字段/数量/安全整数、白名单和独立请求大小上限、流式导入超限取消、并发初始读取不覆盖草稿，以及发布前的旧状态/历史响应失效。前端 lint、TS7、113 个 Node 测试与 Webpack 生产构建通过（仍使用既有 TS6 构建链）。

真实 PostgreSQL 控制面 + 两个 Gateway 二进制 + Console 的隔离联调中，已验证版本发布、409 并发冲突、回滚递增、一个副本离线不能判完成、控制面停机保留旧路由、Gateway 离线缓存重启及恢复确认。Chrome 验证编辑/验证/确认发布、历史比较、回滚保留草稿、冲突提示和重新读取后保留草稿。未使用真实 GPU 推理或生产集群；加权分配和在途快照行为由确定性调度/race 测试验证，全站 Q03 仍独立。

## P05 用户与会话生命周期验收（2026-09-10）

已接入 `/console/users` 的 owner 管理操作、`/console/account` 自助改密/会话撤销、`/operator/operators` 平台运维账户管理与 `/operator/account` 平台账户安全。退出不再只删本地 Cookie：Console 服务端先调用对应 ControlPlane Redis 会话撤销端点，`204/401` 才清 Cookie，`503`、超时或断网保留 Cookie 供重试。租户与平台 Cookie、白名单和跳转继续相互隔离。

纯逻辑测试新增平台账户契约、租户/平台生命周期白名单、owner 管理权限与退出处置矩阵；`pnpm test` 为 134 个用例。`pnpm lint`、TS 7 `pnpm typecheck` 与 `next build --webpack` 已通过，构建清单包含 `/console/account`、`/operator/account` 与 `/operator/operators`。默认 Turbopack 在本机沙箱仍因内部 CSS 子进程绑定端口失败，沿用仓库既有 Webpack 验收路径。真实 Chrome 连接临时 PostgreSQL/Redis/ControlPlane 与生产构建 Console，验证租户 owner 登录、目标用户管理控件、账户安全页、独立平台登录、运维账户页，以及两个标签页的租户/平台会话同时有效；高影响的改密/禁用最终提交由真实 HTTP e2e 覆盖，未在 GUI 重复执行。

## 预充值计费 Console 接入（2026-10-29）

后端（控制面 `internal/billing`、`internal/logic/billing.go`、`internal/handler/billing.go`）已完成，本次只改 Console，不改 Go 代码、不引入 `dodopayments` npm 包。

| 层 | 落点 |
| --- | --- |
| 契约与换算 | `lib/console/billing.ts`：镜像 `internal/types/billing.go` 的全部响应解析（结账/门户链接必须是绝对 https URL，否则按契约违例拒绝）；micro 点与货币最小单位的字符串精确换算；充值包、单价、余额调整的前端校验（与 `packageFromInput`/`PutModelPrice`/`AdjustTenantCredit` 规则一致）；有界轮询 `pollUntilSettled`（`read`/`sleep` 可注入） |
| 白名单 | `lib/console/upstream-routes.ts`：租户 5 条（`tenant_id` 不透传）、运维 11 条（列表筛选含 `tenant_id`，删除单价只透传 `model`）；Dodo Webhook 不在任何一张表里 |
| 权限 | `lib/console/permissions.ts` 新增 `canPurchaseCredits`（owner/admin），镜像控制面 `Actor.canPurchase` |
| 页面 | 租户 `/console/billing`（`page.tsx` 服务端读会话与回跳参数、`billing-view.tsx`）；运维 `/operator/billing`（五个标签页）；共用 `components/console/billing-tables.tsx`。两侧导航各加一项 |
| 读取状态 | `useResource`/`usePagedResource` 新增只读字段 `errorKind`，计费页据此把 `404` 渲染为「计费未开启」；其余页面行为不变 |
| 文案 | 8 种语言的 `tenant.billing*` 与 `operator.billing*` 词条 |
| 测试 | `lib/console/billing.test.ts`（解析、拒绝、单位换算、范围校验、草稿构造、回跳识别、轮询上界/提前停止/失败计次/取消）；白名单与权限测试补充计费用例；e2e `console.spec.ts` 页面矩阵加入两个页面，`e2e/billing.spec.ts` 覆盖自定义金额换算与请求体、固定包不带金额、`503` 固定文案、回跳轮询等待服务端确认、`404` 空状态、`402` 提示，以及运维新建 JPY 充值包/设置单价/调整余额的线上整数 |

不变量：页面从不根据回跳 URL 的 `status` 判定到账；轮询每 3 秒一次、最多 10 次，订单离开 `pending` 即停止；结账、门户、单价、模式切换与余额调整都只发送一次，不自动重试。

## 支付宝（一人付）兑换（2026-11-11）

租户计费页新增「支付宝」分区（`components/console/alipay-billing.tsx`），只在 `GET /admin/v1/billing` 的 `payment_methods.alipay` 为真时出现；`payment_methods.dodo` 为假时隐藏充值包、订单、预售与客户门户（旧控制面不发该字段时按只有 Dodo 处理；2026-11-12 起易支付开启时这些分区也会出现，见下文「易支付（EZFP）接入」）。分区展示控制面读到的支付宝剩余额度、兑换比例、套餐（价格以支付宝字符串原样展示）、兑换表单、待确认兑换与兑换记录。契约在 `lib/console/alipay.ts`，白名单新增租户 5 条（`/admin/v1/billing/alipay`、`/purchases`、`/redemptions` 的 GET/POST、`/redemptions/:id/refresh`，均不透传身份参数）。运维计费页在只配支付宝时把「充值包」分区显示为「未配置 Dodo」（2026-11-12 起为「未配置 Dodo 或易支付」）而非「计费未开启」。账目类型新增 `alipay_topup`。

不变量：浏览器从不持有一人付 Key，也不传用户、价格或点数，请求体只有 `action_id`、`sku_id`、`amount`。购买与兑换只发送一次，不自动重试；答复丢失（网络、超时、5xx）后再次点击沿用同一 `action_id`，控制面恢复原请求，确定答复后才换新编号。购买入口只做 https 校验后跳转；带 `alipay_return` 回跳时只有限次数重新读取额度，从不据此判定到账。兑换后先读到新概览再替换旧数据，结果提示保留在重新加载的区块之外。

验证：Node 单元测试 347 项、lint、TS7 类型检查与生产构建通过；本机 Chrome 计费专项 17 项通过（新增「丢失答复后同一 action id 重试成功并跳转支付宝入口」）。真实一人付与支付宝联调未进行。

**预售额度不足时先买套餐（2026-11-12）**：预售卡片在已知支付宝剩余额度不足时，「用支付宝额度购买」改为先购买套餐：`presaleTopUp`（`lib/console/alipay.ts`）选出与概览同单位、单档额度即可补足差额的最便宜套餐（一人付只能整档购买，不能指定数量），确认后在浏览器 `localStorage` 记下预售与额度（`PresaleIntent`，1 小时有效，只用于本页自动续办，不作任何凭证），再经既有 `POST /admin/v1/billing/alipay/purchases` 打开支付宝入口。带 `alipay_return` 回跳且存在意图时，计费页切到预售分区，最多约 2 分钟（3 秒 × 40 次）读取额度，额度足够后先丢弃意图、再以新 `action_id` 调用一次 `POST /admin/v1/billing/presales/:id/alipay`，不自动重试；超时或失败时提示到账后再按按钮，额度留在支付宝。没有能补足差额的套餐，或控制面不允许购买时，按钮停用并提示联系运维。在支付宝分区直接购买套餐会清除意图，避免该次回跳误付预售。存储不可用时不自动续办，额度到账后由商户再按一次按钮。不新增后端接口或白名单。验证：Node 单元测试 354 项、lint、TS7 类型检查与生产构建通过；真实一人付与支付宝联调未进行。


## 易支付（EZFP）接入（2026-11-12）

易支付（彩虹易支付 V2）作为第二个法币服务商与 Dodo 并存。控制面在同一变更中实现了下表「契约」中的字段与路由（见 [ControlPlane STATUS「易支付（EZFP）」](../aiServeWeaveControlPlane/STATUS.md#易支付ezfp)，其 e2e 校验了同一份 JSON 形状）；浏览器用例仍以模拟的控制面答复运行，**未与运行中的控制面在浏览器里联调**。Console 不持有易支付的商户号或密钥，不引入任何易支付 SDK，不接收易支付的异步通知（它直接打到控制面 `GET`/`POST /billing/v1/ezfp/notify`，不在任何白名单里，白名单测试新增了它的反向用例）。易支付只收人民币：只有币种为 `CNY` 的商品能经它支付，租户可选支付宝（`alipay`）、微信（`wxpay`）或 USDT（`usdt`），哪些类型可用由运维配置，Console 只展示控制面报告的结果。

| 层 | 落点 |
| --- | --- |
| 契约 | `lib/console/billing.ts`：`payment_methods` 新增 `ezfp`（缺席为 `false`）与 `ezfp_types`（缺席为 `[]`，未知类型与重复项丢弃）；充值包新增 `dodo_available`（缺席为 `true`）与 `ezfp_available`（缺席为 `false`）；订单新增 `provider`（`dodo`/`ezfp`/`opcpay`，缺席时 `alipay_amount > 0` 推断为 `opcpay`，否则 `dodo`；本 Console 不认识的值原样保留并按原值显示，较新的控制面不会让订单列表失效）与 `pay_type`；`lib/console/account-sales.ts` 账号新增 `dodo_available`（缺席时已标价为 `true`）与 `ezfp_available`；`lib/console/account-presales.ts` 预售新增 `ezfp_available`（缺席为 `false`）。`available` 仍只代表「经 Dodo 可购买」，只有 `available`、`ezfp_available`、`alipay_available` 全为假才显示「已售罄」 |
| 付款选择 | 新增纯函数模块 `lib/console/payment-choice.ts`：`PayChoice`、`payChoices(methods, 商品可用性)`（顺序为 Dodo，再依次是已开启的易支付类型）、`withPayChoice(body, choice)`（**只有选易支付时**才在请求体加 `provider:"ezfp"` 与 `pay_type`，Dodo 两者都不发，旧控制面会以 400 拒绝未知字段）。共用组件 `components/console/pay-choice-buttons.tsx` 用于充值包、账号现货卡片与预售卡片；只有 Dodo 一种选择时与此前完全一致（文案、行为），多种选择时显示「银行卡 / Dodo」「支付宝」「微信支付」。账号与预售的确认对话框携带所选 `PayChoice`，选易支付时写明「支付方式：…」。预售上原有的「用支付宝额度购买」（一人付额度）保持独立、文案不同的按钮 |
| 白名单 | `lib/console/upstream-routes.ts` 租户表新增 1 条 `POST /admin/v1/billing/orders/:id/refresh`（无请求体、不透传任何查询参数）。`/billing/v1/ezfp/notify` 不在任何表里 |
| 回跳 | 易支付回跳到 `/console/billing`（我们自己不带查询参数），并附 `pid, trade_no, out_trade_no, type, trade_status, name, money, param, buyer, timestamp, sign, sign_type` 等。`ezfpReturnOrderId(params)` 只在 `out_trade_no` 匹配 `^ord_[0-9a-f]{28}$` 且 `trade_no` 非空时返回订单 id。命中后 `billing-view.tsx` 立即 `history.replaceState` 为 `/console/billing?tab=orders`（`sign`、`buyer`、`money` 等不留在地址栏与历史记录），只发送**一次** `POST /admin/v1/billing/orders/:id/refresh`（失败与答复都忽略），随后沿用 Dodo 回跳的有界轮询（概览与 `orders?limit=1`，3 秒 × 10 次）与同一条 `ReturnNotice`。`trade_status` 与其余参数从不被读取、从不当作付款证明 |
| 订单表 | `components/console/billing-tables.tsx` 新增「支付方式」列（Dodo / 易支付 · 支付宝 / 易支付 · 微信 / 支付宝额度）；租户面上待支付的易支付订单有「再次确认」按钮，每次点击只发送一次 refresh 并用答复更新该行（仍待支付时提示稍后再试，间隔限制由控制面执行），读取新一页后以服务端行为准 |
| 运维表单 | 充值包、账号、预售的 Dodo 产品 ID 在币种为 `CNY` 时可留空（提示「币种为 CNY 时可留空，仅能经易支付购买」），非 CNY 仍必填（预售保留「支付宝额度数量大于 0 时可留空」的既有例外）。控制面在 Dodo 或易支付任一配置时挂载充值包管理，因此运维「充值包」分区的不可用提示改为「未配置 Dodo 或易支付」 |
| 文案 | 8 种语言新增 `tenant.payChoice*`、`tenant.payMethodLine`、`tenant.billingPayMethod`、`tenant.billingProvider*`、`tenant.billingRecheck*`、`tenant.billingMethodUnavailable`、`tenant.accountProductOptionalHint`、`operator.billingProductOptionalHint`，并改写 `operator.billingNeedsDodo*`；`catalog.test.ts` 检查各语言键与占位符一致 |
| 测试 | `payment-choice.test.ts`（选项顺序、可用性组合、请求体）；`billing.test.ts`（`payment_methods` 与充值包标志的默认值、订单 `provider`/`pay_type` 读取与推断、结账请求体、`ezfpReturnOrderId` 的接受与拒绝、CNY 可不填产品）；`account-sales.test.ts`、`account-presales.test.ts`（可用性标志与默认值、请求体、CNY 可不填产品）；`upstream-routes.test.ts`（refresh 路由、方法、遍历、易支付通知不转发）；e2e `billing.spec.ts` 新增 9 项：选微信只发一次 `{package_id, provider:"ezfp", pay_type:"wxpay"}` 并跳转、Dodo 点击不带 `provider`、仅 Dodo 可售的商品保持单个「购买」、仅易支付部署的分区、易支付回跳清理 URL 并只发一次 refresh 且提示由 pending 变为已确认、缺订单 id 或交易号的参数视为普通访问、「再次确认」每次点击一次请求并更新行、仅易支付的 CNY 预售不显示「已售罄」且请求体正确、CNY 账号走易支付、运维可保存无产品 ID 的 CNY 充值包而其他币种被拦下 |

不变量：Console 不持有易支付凭据，不接收易支付通知；回跳参数只是提示，到账与订单状态一律以控制面记录为准；结账与 refresh 都是只发送一次的写请求，不自动重试；只有 refresh 白名单一条新路由。

验证记录见本节末「验证结果」。未验证：真实易支付商户、真实控制面的 `provider`/`pay_type`/`*_available` 字段与 refresh 限速行为、真实支付后的回跳参数形状；多语言文案（除 zh-CN/zh-TW/en-US 外）为译文，未经母语者审校。

验证结果（2026-11-12，本机）：`node --test "lib/**/*.test.ts"`（即 `pnpm test`）375 项通过；`eslint`（即 `pnpm lint`）0 错误，仅有既有的 `alerts-view.tsx` React Compiler 警告；`tsc --noEmit`（即 `pnpm typecheck`，TS 7）通过；`next build`（即 `pnpm build`）通过；本机 Chrome `AISW_BROWSER_CHANNEL=chrome playwright test e2e/billing.spec.ts` 26 项通过（含新增 9 项）。同时修正了既有用例「operators create a presale…」对 `alipay_amount: 0` 的遗漏断言。全套浏览器用例 432 项通过、5 项失败（`console-header.spec.ts` 的 320px 与 1024px 两项，`console.spec.ts` 中 320px 的 `/operator/routes`、`/operator/workflow-templates` 与「navigation marks only the current tenant page」）；在未含本次改动的 HEAD 上单独构建后复现了同样 5 项，与本次无关。

## Agent 接入下载

新增运维导航「Agent 接入」与 `/operator/agent-enrollment` 页面。平台运维可单独下载公共 CA，或填写 Registry/Gateway 的外部地址和有效期，生成一个随机新节点专用的 ZIP 交给节点提供者。对方无需平台账户或平台服务器权限。包内安装说明覆盖 Ubuntu、Apple Silicon macOS、原生 Windows；Agent 发行包另行取得，保持平台同版本和实际验收范围。

GET/POST 均通过 Operator 明确白名单调用控制面；生成不自动重试。响应大小限制为 128 KiB，客户端校验证书格式、摘要、节点 ID、固定文件名与 ZIP/base64 大小；接入凭据只留在当前页内存与用户主动下载的文件，不写 URL、localStorage 或日志。离开后不能找回原包。页面文案覆盖八种语言，写操作沿用同源检查、有界请求体和平台会话隔离；租户不能调用此接口。

本地验证与尚需真实远程接入验收的范围见 [验证记录](../../docs/acceptance/agent-enrollment-2026-10-30/README.md)。

## 节点托管供应商面板（2026-11-02）

商户 `/console/nodes` 的每台未删除节点卡片里新增可折叠的「托管 AI 接口」面板，平台运维新增 `/operator/node-providers`（输入平台节点 node_id，拒绝 `tnode.`）。两者共用 `components/console/node-providers-panel.tsx`，分别走 `/admin/v1/nodes/:node_id/providers[/:runtime_id]` 与 `/operator/v1/nodes/:id/providers[/:runtime_id]`（白名单各三行）。Key 输入框只写不读，只展示后端返回的末 4 位；面板显示版本、节点是否在线、Agent 回报结果与逐项状态。写入只发一次、不自动重试。契约镜像与本地预检在 `lib/console/node-providers.ts`（镜像 `common/remoteprovider` 的 ID 与端点规则）。lint、构建、TS7/TS6 类型检查与单元测试通过；未做浏览器视觉检查与八语言译文审校。

## Agent 一行命令安装面板（2026-11-01）

运维「Agent 接入」与商户「节点管理」新增共用的「添加节点」面板（`components/console/agent-install-panel.tsx`）：选择系统（Ubuntu、macOS、Windows）、凭据方式（兑换码、交互输入、令牌直写）、下载来源（平台、GitHub）与有效期，调用控制面 `POST /operator/v1/agent-install-codes` 或 `POST /admin/v1/nodes/install-codes`，生成一行命令与复制按钮。原 ZIP 接入包收进「高级」折叠区。

响应由 `lib/console/agent-install.ts` 严格校验（兑换码格式、节点 ID、令牌字符集、地址、HTTPS 站点根），命令里的所有值用单引号引用；两条路由写入 `upstream-routes.ts` 白名单且丢弃查询参数，签发不自动重试，凭据只留在页面内存。控制面未配置 `AgentInstall.PublicURL` 时（`404`）显示「未启用」。签发成功后，若平台地址就是本站来源，面板用浏览器读取公开的 `/agent/v1/manifest`（`credentials: omit`，5 秒超时），平台版本被安装脚本拒绝（`installable: false`）或入口不可达时在命令上方显示红色警告；跨域地址不检查，避免误报。文案覆盖八种语言。

默认测试（`agent-install.test.ts` 等，Console 共 312 项）、`eslint`、`next build`、`tsc` 通过。未验证：浏览器渲染与剪贴板、Playwright e2e、真实控制面端到端、八种语言译文审校。部署与使用见 [部署说明](../../deploy/README.md#接一个-agent)。

## Agent 账号独享包月页面（2026-11-09）

商户与平台说明已明确 Claude Code / Codex 两种账号都支持独享售卖，同一个 Agent 中的账号可分别定价和售给不同商户；文案同步八种语言。

商户计费页新增账号库存、到期时间、余额购买与续费；购买确认明确提示切换为仅允许已购账号的包月模式。平台计费页新增「账号包月」标签页，管理账号商品及月价；新增商品先选择 Agent、再选择实际上报的 Claude Code / Codex 账号，无需手填 ID；排除已有商品及商户自有节点，空清单与读取失败分别提示。已建商品的 Agent/运行时 ID 只读。账号接口 404 显示「未开启账号包月」，运维可看到独立开关配置提示。商户模式列表支持 `account_monthly`，账目支持 `account_purchase`。库存分页面展示，文案补齐八种语言；凭据始终留在 Agent。新接口分别列入 Admin/Operator 白名单，写入不自动重试，确认框内的人工重试复用同一次购买标识。

验证：ESLint 无错误（现有 alerts 页 TanStack Virtual 警告仍在）、TS7 类型检查、323 项 Node 测试及 Next.js Webpack 生产构建通过；现有 Chrome 完成计费专项 11 项，涵盖实际账号类型选择、保存目标、固定身份编辑、重复库存过滤、关闭配置提示和原有购买/续费流程。浏览器使用隔离会话和模拟 API，不能替代真实控制面/付款/CLI 的生产验收。使用与升级说明见[账号包月](../../docs/billing-accounts.md)。

**法币标价（2026-11-11）**：账号卡片按币种显示月价（`formatMoney`），旧版无法币价格的商品显示「未设定法币价格」且不可购买。购买/续费确认后调用 `POST /admin/v1/billing/accounts/:id/checkout`（白名单替换原 `/purchase`），只发送确认时的 `expected_price_minor` 与 `currency`，不自动重试，成功后跳转控制面交回的 https 结账地址；`409`/`404`/`503` 分别提示账号不可购买、在线支付未开启与支付服务暂不可用。回跳等待确认后账号卡片随历史记录重挂载。运维编辑器改为填写币种、主单位月价与 Dodo 产品 ID（`accountRequest` 镜像控制面校验）。订单列表「购买内容」列对账号订单显示开通到期时间或「账号未开通，待退款」。新增文案补齐八种语言；`tenant.accountUnavailable` 等账号错误文案补入同步 core 词典（此前 409 会显示「暂不可用」）。验证：ESLint 无错误、TS7 类型检查、328 项 Node 测试、生产构建及 Chrome 计费专项 12 项通过；完整 Playwright 套件 420 项通过，`console-header` 两项与 `console.spec` 导航一项在改动前的代码上同样失败，与本改动无关。

## 运维 Agent 共享设置（2026-11-09）

Agent 接入页面新增默认不勾选的「共享给所有租户」，安装命令与 ZIP 接入都发送明确选择。机群审批确认框提供同一开关，并显示已保存的共享/私有/指定租户状态；机群页可进入共享设置。节点访问编辑器支持不共享、共享和指定租户三种模式，新 Agent 默认显示不共享。租户没有修改入口。文案同步八语言。

验证：326 项 Node 默认测试通过，ESLint 无错误（现有 TanStack Virtual 警告保留）、TS7 检查与生产构建通过。Chrome 新增共享操作及 Agent 接入回归通过，并通过现有运维页面 1440/390/320 px 检查。本地 Docker 已实际登录验证添加页的默认不共享选项、共享设置入口和 API；租户模型目录隐藏未共享 Agent。浏览器回归使用隔离会话和模拟 API，未调用真实 CLI 生成。操作见[Agent 共享设置](../../docs/agent-sharing.md)。


### 公开价格页 /pricing（2026-11-10）

任何人无需登录即可打开 `/pricing`，查看账号包月、点数充值包与模型单价。数据由服务端组件直接读取控制面公开的 `GET /billing/v1/catalog`（不带令牌，不经 `/api/admin/*` 白名单）；控制面未开启计费、不可达或响应不合契约时显示「购买暂未开放」。「购买」按钮是普通链接：有会话 Cookie 时直达 `/console/billing`，没有时跳转 `/login?next=/console/billing`，登录后回到计费页；每次购买仍由控制面授权。`AISW_CONSOLE_SIGNUP_ENABLED` 开启时页头显示注册入口。页面覆盖根布局的 noindex，允许被索引。`proxy.ts` 的 matcher 不含 `/pricing`，天然公开。契约镜像为 `lib/console/billing.ts` 的 `parseCatalog`，八语言文案在 `pricing.*`。

法币标价合并后（2026-11-11），`/pricing` 的账号卡片按 `price_minor` + `currency` 显示法币月价，`parseCatalog` 拒绝零价或非三位大写币种；控制面只开账号包月、未开在线支付时目录不列账号。

账号预售（2026-11-11）：平台计费页新增「账号预售」标签页，包含预售商品（类型、名称、币种、月价、Dodo 产品 ID、名额、上架；显示「待发货 x / 名额 y」）和「待发货订单」两块。待发货订单经 `fulfillment=awaiting` 分页读取，每行可以「分配账号」：对话框只列出同类型、不在商户自有节点、空闲或已属于该商户的库存，最终仍以控制面判断为准。也可以「退款」，填写原因后全额退款，这是 Console 首个运维退款入口。商户计费页在有预售时显示「账号预售」区块，售罄时禁用购买；订单表显示「已付款，待发货」，支付回跳确认的是预售订单时，提示账号将在数小时内分配。`/pricing` 增加预售区块。账号编辑器创建时带上 Agent 上报的类型，旧商品可以补填类型，下架时允许不填价格与产品。`ConfirmDialog` 新增可选的 `children` 与 `confirmDisabled`。契约镜像在 `lib/console/account-presales.ts`；新增 6 条白名单与 3 个错误类型，八语言文案齐备；所有写操作只发送一次。

商户计费页布局整理（2026-11-11）：页头右侧放「管理付款与收据」（门户提示改为按钮 `title`），下方是精简的余额 / 计费模式摘要（「只有 owner 与 admin 可购买」的提示移到这里），其余内容分为「充值包 / 账号现货 / 账号购买 / 订单 / 账目」标签页；「账号购买」（预售）只在有预售商品时提供，其读取提到页面层，支付回跳确认后重新读取。当前标签写入查询参数 `tab`（`window.history.replaceState`，不触发服务端重渲染、不占后退历史），刷新或分享链接时保持不变；没有 `tab` 时，支付回跳打开订单，`account_monthly` 商户打开账号现货，其余打开充值包；指定了未提供的分区时同样回退。账号现货区分「我的账号」与「可购买的账号」：既未持有也未上架的账号不再列给商户；搜索框只在可购买账号超过一页（12 个）时出现，分页控件只在多于一页时出现；商户侧去掉「刷新账号」按钮。运维账号视图与之前相同。卡片按钮不再占满整行，名称最多显示两行。中文界面把商品区块「账号包月」改名为「账号现货」、「账号预售」改名为「账号购买」（`tenant.accountMonthly`、`tenant.presale`、`pricing.accounts`、`pricing.presales`，简繁两种，运维标签页与 `/pricing` 一并生效）；计费模式名称拆为独立的 `tenant.billingModeAccountMonthly`，仍显示「账号包月」。新增八语言文案 `tenant.accountMine`、`tenant.accountAvailable`、`tenant.accountNoneAvailable`、`tenant.billingSections`。lint、typecheck、单元测试、build 与 `e2e/billing.spec.ts`（16 项）通过；`console.spec.ts` 中「navigation marks only the current tenant page」（`/console/jobs/history`）在改动前就已失败，与本次无关。

验证：ESLint 无错误（仍有既有的 alerts 页 TanStack Virtual 警告）、TS7 类型检查、338 项 Node 测试与生产构建通过。使用本机 Chrome（`AISW_BROWSER_CHANNEL=chrome`）跑计费专项 15 项全部通过，新增商户预购、售罄与 409 不重试、运维新建预售并按类型分配三项。`console.spec.ts` 80/81 通过，失败的 `/console/jobs/history` 导航高亮用例与本次改动无关，未在本次处理。浏览器测试使用模拟 API，不能替代真实控制面、付款与部署验收。

验证：Node 默认测试、ESLint、TS7 类型检查与生产构建通过；用模拟控制面本地启动，未登录访问 `/pricing` 返回 200 并渲染套餐/账号，购买链接为 `/login?next=%2Fconsole%2Fbilling`，注册入口按开关显示。未用真实控制面、真实 Dodo 付款或浏览器做 1440/390/320 px 布局检查。

### 机群页直接控制共享（2026-11-10）

`/operator/fleet` 每个平台注册节点的操作行新增「开启共享 / 停止共享」。点击后显示节点及影响说明，确认才写入现有 `PUT /operator/v1/node-access/:node_id`；开启专属节点共享时明确提示替换租户绑定。写入失败不自动重试，完成或失败后重新读取状态；读取当前策略失败时禁用操作。商户自有节点不显示该按钮。刷新节点状态时同时刷新共享状态，八语言文案已同步。

验证：326 项 Node 测试、TS7 类型检查与生产构建通过；8 项 Chrome 检查覆盖开关、失败不重试、专属替换提示、策略读取失败、原审批行为及 1440/390/320 px 布局。已更新本地 Docker Console，实际登录机群页验证节点行按钮和确认/取消交互；该本地检查未提交共享变更。
