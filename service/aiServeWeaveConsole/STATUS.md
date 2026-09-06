# Console 开发状态与任务规划

更新日期：2026-09-06。M1–M3 已完成；M4 完成只读的 C21/C23，M5 完成 C25 与 C26 的实时部分（其余未做，理由见各节）。本文记录 `service/aiServeWeaveConsole` 的现状、实施顺序、后端依赖与验收标准；任务尚未实现时保持未勾选，不将依赖安装视为业务功能完成。

## 目标与范围

首版面向单个租户内的 owner、admin、member，完成登录、用户列表与创建、API Key 生命周期、管理审计的真实接口闭环。配额编辑在补齐读取接口后交付。节点、模型、路由、工作流和监控按后端 Admin API 的成熟度逐步接入。

Console 只调用控制面的 Admin API，不直连 Gateway、Registry、Agent、推理后端或数据库。租户引导由运维完成，首版不提供持有 BootstrapToken 的注册页、跨租户管理或租户切换。

## 当前状态

| 模块 | 状态 | 已核实的内容 |
| --- | --- | --- |
| 工程基础 | 已就位 | Next.js 16 App Router、React 19、Tailwind CSS 4、pnpm |
| UI 基础 | 已就位 | `components/ui/` 已有按钮、输入、表格、对话框、标签页、骨架屏等组件；`lib/utils.ts` 使用 `cn` 包 |
| 表格与图表依赖 | 已安装，未接业务 | TanStack Table、TanStack Virtual、ECharts、`echarts-for-react` |
| TypeScript 与门禁 | 已配置 | TS 6 / 7 并存；已有 `lint`、`typecheck`、`build` 脚本，不代表本次已运行通过 |
| 页面 | M1 已就位 | 登录页 `app/login/`、受保护布局与概览页 `app/console/`；根布局已改为中文站点元数据 |
| 登录、会话与 API 客户端 | 已落地 | `/api/session` 与 `/api/admin/*` 服务端入口、AES-256-GCM 密封的会话 Cookie、`lib/console/api-client.ts` |
| 业务测试 | 已建设 | Console `pnpm test` 走 Node 内置 `node:test`，61 个用例，无新增依赖；控制面新增分页、筛选、租户读取、机群与工作流聚合的 Go 测试及 e2e 隔离断言，Gateway 新增运维端点测试 |
| 使用文档 | 已重写 | README 记录调用链路、环境变量、门禁与部署前置条件 |
| 业务页面（用户/Key/审计/配额） | M2、M3 已就位 | `app/console/users`、`keys`、`audit`、`quota`，均对接真实 Admin API；三张列表为服务端游标分页与筛选 |
| 运维页面（节点/模型/发布状态） | M4、M5 已就位 | `app/console/fleet`、`models`，以及 `workflows` 的运维区块；只读，只在配置了运维模式的部署中存在，且只对名单内的人存在 |
| 工作流与运行（租户） | M5 已就位 | `app/console/workflows`（菜单，不含图）、`app/console/jobs`（实时视图，非历史） |

实现时遵守 [Console 开发约定](AGENTS.md) 与 [仓库开发约定](../../AGENTS.md)。技术栈沿用现有选型；动 Next.js 代码前先读本地 `node_modules/next/dist/docs/`，不在本规划中引入依赖升级。

## 已有接口与真实边界

接口依据：[路由](../aiServeWeaveControlPlane/internal/handler/routes.go)、[请求与响应结构](../aiServeWeaveControlPlane/internal/types/types.go)、[HTTP 处理](../aiServeWeaveControlPlane/internal/handler/handlers.go)、[用户与权限逻辑](../aiServeWeaveControlPlane/internal/logic/service.go)、[Key 与配额逻辑](../aiServeWeaveControlPlane/internal/logic/apikeys.go)。以下是当前实现，后续接口变化需同步更新。

| 能力 | 已有接口 | Console 接入边界 |
| --- | --- | --- |
| 登录 | `POST /admin/v1/auth/login` | 返回 `token`、`expires_at`、`user`；没有 refresh、logout 或当前用户查询接口 |
| 用户 | `GET/POST /admin/v1/users` | 登录用户可读本租户列表；仅 owner 可创建；未提供编辑、删除、禁用、改密接口 |
| API Key | `GET/POST /admin/v1/apikeys`、`DELETE /admin/v1/apikeys/:id` | 登录用户可读本租户列表；owner/admin 可创建与吊销；member 仅可吊销自己创建的 Key |
| 租户配额 | `PUT /admin/v1/tenants/limits` | owner/admin 可写本租户的整组限制；没有读取当前配额的 Admin API |
| 审计 | `GET /admin/v1/audit?limit=...` | 当前所有登录角色均可读取本租户审计；只有 `limit` 参数，没有游标、分页总数或服务端筛选 |
| 租户引导 | `POST /admin/v1/tenants` | 需要 BootstrapToken；不接入常规 Console 会话 |
| Gateway 校验 | `POST /internal/v1/apikeys/verify` | 需要 InternalToken；Console 不调用、不持有该密钥 |

当前用户与 Key 列表返回数组；吊销成功返回 `204`；错误体为 `{ "error": "..." }`。不能套用未经核实的统一分页响应结构。

### 需要跟踪的文档差异

- 根目录 AGENTS 的代码地图仍写“控制面配额未做”，当前代码已有写入与下发能力；真正阻塞 Console 编辑闭环的是读取接口。
- 控制面 README 将 member 概括为“管理自己的 Key”，但当前创建逻辑仅允许 owner/admin；列表逻辑向 member 返回本租户全部 Key 的展示信息。
- 控制面 README 提到 owner 增删用户，但路由当前只支持列表与创建。
- 控制面 README 概括越权为 `404`，实际角色不允许的操作返回 `403`；资源不存在、跨租户及 member 吊销他人 Key 返回 `404`。前端要分别处理，不能把所有 `404` 推断为权限问题。

- 吊销不幂等：`RevokeAPIKey` 经由 `store.RevokeAPIKey` 匹配 active 行，重复吊销返回 `404`，与「他人租户的 Key」「不存在的 id」同码。前端无法区分三者，只能刷新列表后说明状态已变。这一点当前文档均未写明。

上述差异作为文档修正或产品权限调整任务记录，页面先与实际后端行为对齐；若要改变授权范围，必须由控制面先实现并测试。

## 实施顺序

| 阶段 | 优先级 | 交付目标 | 依赖与退出条件 |
| --- | --- | --- | --- |
| M1 基础与会话 | P0 | 可登录、刷新、退出的控制台框架 | 现有登录 API；通过会话与请求隔离验证 |
| M2 管理闭环 | P0 | 用户、API Key、近期审计 | M1；真实接口完成关键操作与角色验证 |
| M3 配额与数据规模 | P1 | 可读写配额、可分页查询 | 控制面新增读取与分页契约后交付 |
| M4 推理资源管理 | P2 | 节点、运行时、模型、路由 | 对应控制面聚合与管理 API |
| M5 工作流与可观测性 | P2 | 模板、Job、产物、指标 | Admin API、持久化与指标查询能力 |

M1–M3、M4 的只读部分与 M5 的 C25/C26 实时部分已完成（见各节勾选项与验收记录），其余仍待后端能力，M3 的后端缺口可提前排期。M4/M5 不阻塞首版管理闭环；没有数据来源的菜单暂不开放，不用示例数据冒充运行状态。

## M1：控制台基础与会话（P0）

- [x] **C01 页面框架**：替换欢迎页与默认元数据；以中文为首版界面语言，建立登录页和受保护的管理布局，提供导航、当前用户、角色、租户 ID 与退出入口。没有租户详情接口时不编造租户名称。
- [x] **C02 请求与会话边界**：采用浏览器 → Next.js 同源服务端入口 → ControlPlane Admin API 的方案。服务端固定上游地址并显式限定方法和路径，不做任意 URL 代理；浏览器无法指定上游 Host 或注入服务间凭据。
- [x] **C03 登录与会话生命周期**：登录令牌存入 HttpOnly Cookie，生产开启 Secure，设置合适的 SameSite 与到期时间；写操作校验来源并落实 CSRF 防护。明确刷新页面时的用户信息恢复方案，身份只能来自可信会话，不能信任浏览器可修改的角色值；鉴权最终由控制面执行。
- [x] **C04 退出与失效**：退出清理 Cookie 和用户数据缓存；过期或受保护请求返回 `401` 时转到登录页；只允许站内返回地址。当前退出仅清理 Console 会话，不宣称已撤销后端 JWT；不虚构自动续期能力。
- [x] **C05 统一 API 客户端**：依据 Go JSON 契约维护类型与响应校验；覆盖数组、可选字段、日期、`204`、`400/401/403/404/409/5xx`、断网与超时。请求可取消，读请求重试有上限，创建 Key、创建用户等写请求不自动重试。
- [x] **C06 共享交互**：统一表格、表单错误、加载、空数据、失败重试、确认对话框与通知。表单提交期间防重复点击；只读查询失败不呈现为“零条记录”。
- [x] **C07 数据与凭据保护**：认证响应、业务响应不进入共享缓存；令牌、密码、Key 明文不进入 localStorage、URL、日志、异常文本或分析事件。前端只采用必要字段和固定错误文案；涉及 Go 日志时遵守 `runtime.Redact` 约定。

**实现位置**

| 任务 | 落点 |
| --- | --- |
| C01 | `app/layout.tsx`（中文元数据、`robots: noindex`）、`app/login/`、`app/console/layout.tsx`、`app/console/console-shell.tsx`、`app/console/page.tsx`。导航当前只有「概览」，用户/Key/审计随 M2 视图落地再加菜单项；租户只显示 ID，不编造名称 |
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

## M4：节点、运行时、模型与路由（P2，待 Admin API）

| 任务 | Console 交付 | 必须先具备的后端能力 |
| --- | --- | --- |
| [x] C21 节点与运行时 | 节点列表、在线状态、最近心跳、标签、运行时与能力详情；缺失指标显示不可用 | ControlPlane 聚合节点快照、权限过滤、数据时间戳；CPU/GPU 等字段须有真实上报来源 |
| [ ] C22 节点管理 | 接入说明、审批、禁用、维护状态与操作审计 | 正式管理接口与生效路径；不能用 Console 操作直接替代 Registry 内部能力 |
| [x] C23 模型与部署目录 | 区分逻辑 Model、Backend、Deployment，展示能力与实际可用部署 | 租户可见目录和部署状态查询；明确快照与持久化数据的关系 |
| [ ] C24 路由配置 | 编辑真实模型映射、节点选择器、优先级与权重，校验并展示生效结果 | 路由持久化、版本、发布/回滚、Gateway 同步与审计；当前文件配置不等于管理 API |

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

## M5：工作流、Job 与可观测性（P2，待后端能力）

| 任务 | Console 交付 | 必须先具备的后端能力 |
| --- | --- | --- |
| [x] C25 工作流模板 | 模板列表、输入定义、节点/模型依赖、校验结果与版本 | 受租户授权的模板管理 API、版本与校验接口；完整工作流 JSON 不进日志或错误提示 |
| [ ] C26 Job 与产物 | Job 列表、详情、进度、取消、产物预览与下载 | ControlPlane 的 Job 管理入口、持久化、分页、授权事件流和产物访问；不能直接复用 Gateway 公共数据面地址 |
| [ ] C27 总览与指标 | 请求量、成功率、延迟、Token 与容量时序；ECharts 支持 `dataZoom` | 控制面授权聚合/查询 API，明确指标定义、时间窗口、单位、采样与租户范围；已有 Prometheus 导出不等于可直接绘制历史曲线 |
| [ ] C28 请求与错误检索 | 按时间、状态、request ID 查询脱敏元数据 | 可检索存储、保留期、分页与权限接口；不得展示或记录完整 Prompt、鉴权头 |
| [ ] C29 告警 | 告警列表、规则与处理状态 | 指标查询、告警计算、规则与通知管理 API；不在浏览器实现唯一的告警判定 |

Job 事件逐条消费，页面离开时取消订阅，断线恢复依赖明确的后端契约；历史事件数量有上限。产物采用授权流式下载或短期链接，避免整文件缓冲。当前 Gateway Job 表为进程内存，不能据此承诺跨副本、重启后可查的任务历史。

### M5 的两个前置决定

1. **可做的只有 C25。** 工作流模板的数据今天真实存在（Gateway 的文件配置）；Job 的持久化历史不存在，指标与请求检索所需的时序库与日志存储仓库里根本没有。选定的范围是 **C25 + 一个实时 Job 视图**：跨副本聚合 Gateway 内存中的 job 表，**不是持久化**，界面必须明说。
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

**未做及理由**：C26 的持久化与产物下载——需要控制面建 jobs 表、Gateway 写生命周期（会把控制面放上推理写路径），产物则由 Gateway 数据面用租户 API Key 提供，本控制台不持有那个 Key；C27–C29——需要时序库与可检索日志存储，仓库里都没有，Prometheus 文本导出不等于历史曲线。四条均记入控制面 README 的已知缺口 8、9。

**验收**：事件流可取消且内存有界；产物受租户授权保护；图表能区分“无流量”和“数据不可用”，显示查询时间与数据新鲜度；完整工作流与 Prompt 不进入日志、异常或遥测。

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

- [x] **Q01 测试基础（M1）**：选用 Node 24 内置的 `node:test` + `node:assert/strict`，**没有新增任何依赖**（理由与两条使用约束见 [AGENTS.md](AGENTS.md)）；脚本为 `pnpm test`，当前 47 个表驱动用例覆盖契约解析（含一次性 Key）、会话密封与过期、转发白名单、来源校验、请求层重试与错误分类、角色矩阵、Key 状态与密码创建与登录回环。`fetch` 与 `sleep` 通过参数注入，过期判定用注入的时间，不依赖真实网络、控制面或 `sleep`。
- [x] **Q02 契约与关键流程（随 M1–M3）**：覆盖登录失效、跨租户切换、角色矩阵、一次性 Key、`204`、失败重试、配额零值/缺省字段、分页响应乱序；测试数据与当前 Go 请求响应一致。
  - Console 47 个用例覆盖：登录失效与 401 转登录、跨租户隔离、角色矩阵、一次性 Key 与 `display` 前缀关系、`204`、读重试与写不重试、配额零值与 `omitempty` 缺省字段（`limits: {}`）、列表信封与 `next_cursor` 的四种取值、游标历史上界、查询参数构造、密码创建与登录回环、Key 过期与吊销状态。
  - 控制面新增 Go 用例覆盖：同一时刻写入行的全序分页（不重不漏）、翻页途中并发写入不影响后续页、单页上限与默认值、无效游标、三类筛选与租户隔离、颠倒/空时间窗、未知角色筛选、配额读写回环。
  - 「分页响应乱序」在浏览器中验证：逐字符快速输入筛选词时旧请求被取消，页面只呈现最终查询的结果（见 M3 验收记录）。

- [ ] **Q03 界面验收（随各阶段）**：检查桌面与窄屏、键盘操作、表单标签、焦点恢复、错误可读性和长文本；大表格使用有上限的模拟数据检查滚动、筛选及内存增长。
  - 已在无头 Chrome（CDP 驱动）中核对：审计虚拟滚动只渲染可见行、本地筛选生效、创建 Key 对话框与一次性明文、关闭后明文离开 DOM 与浏览器存储、吊销确认框内容与吊销后状态、401 转登录并保留返回地址。表单每个输入都有 `label`，筛选框有 `aria-label`，错误用 `role="alert"`。
  - 未覆盖：窄屏布局、纯键盘操作路径、对话框关闭后的焦点恢复、以及万行量级数据下的内存增长。这些需要人工或引入浏览器测试工具，暂不勾选。
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
