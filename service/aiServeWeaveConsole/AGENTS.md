<!-- BEGIN:nextjs-agent-rules -->

# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` (resolved from this file's directory; in monorepos the `next` package may not be visible from the repo root) before writing any code. Heed deprecation notices.

This block is written and re-added by `next dev` — verify at `node_modules/next/dist/server/lib/generate-agent-files.js`. Removing it from a diff only re-creates the uncommitted change; committing it with your work keeps the tree clean.

<!-- END:nextjs-agent-rules -->

# Console 前端约定

以上 block 由 `next dev` 自动维护，改动会被覆盖；本行以下是本项目自己的约定。

## 技术选型

| 用途 | 选择 |
| --- | --- |
| 框架 | Next.js 16 App Router + React 19 |
| 样式 | Tailwind CSS 4 |
| 组件 | shadcn/ui（源码在 `components/ui/`，属于本仓库代码，可直接改） |
| 表格 | TanStack Table + TanStack Virtual（审计日志、Job 队列可能上万行，必须虚拟滚动） |
| 图表 | ECharts（`echarts-for-react`），指标时序曲线用 `dataZoom` |
| 节点图 | `@xyflow/react`（MIT），仅用于运维模板页的只读 ComfyUI 图预览。选它而不是 ComfyUI 官方前端或 `@comfyorg/litegraph`：前者是 GPL-3.0 的 Vue 整站且要直连 ComfyUI 后端，后者自 2025-08 起不再单独发版且只接受带坐标的 UI 格式。布局算法在 `lib/console/comfy-graph.ts` 自写，不再引入 dagre/elk |
| 工作流编辑器 | `wujie` 2.1.0（MIT），以同源微前端挂载官方 ComfyUI_frontend（GPL-3.0，部署时从固定源码应用受控本地化补丁构建，不进本仓库、不进 Console 打包）。只用于运维模板页的可视化编辑；只读预览仍用上一行的 `@xyflow/react`，两者互不替代：预览不依赖控制面配置编辑器，编辑器要求 `ComfyEditor.DistDir`。约束见下文「ComfyUI 编辑器」 |

`cn` 工具函数来自 shadcn 官方的 [`cn`](https://github.com/shadcn-ui/cn) 包，不是 `clsx` + `tailwind-merge` 的手写组合，也不是同名抢注包。

## TypeScript 是 6 / 7 双版本并存，不要"修正"成单版本

`package.json` 里这两行是**刻意**的，不是写错：

```json
"@typescript/native": "npm:typescript@^7.0.2",
"typescript": "npm:@typescript/typescript6@^6.0.2"
```

原因：typescript-eslint 至今不支持 TS 7（见 [typescript-eslint#10940](https://github.com/typescript-eslint/typescript-eslint/issues/10940)），把 `typescript` 直接装成 7.x 会让 `pnpm lint` 整个报错退出，而不是只报几条警告。所以按 TypeScript 官方的 side-by-side 方案，让 `typescript` 这个名字解析到 TS 6 的 API 包供 typescript-eslint 与 `next build` 使用，TS 7 则通过 `@typescript/native` 单独提供。

由此产生三个可执行文件，用途不同：

- `pnpm typecheck` → `tsc`，即 **TS 7**，原生 Go 编译器，日常类型检查用它（项目初建时曾测得约 4 倍速度差，不作为当前性能保证）。
- `pnpm exec tsc6` → **TS 6**，与 `next build` 内部所用版本一致。
- `pnpm lint` / `pnpm build` → 内部走 TS 6。

**因此 `pnpm typecheck` 通过不等于 `pnpm build` 通过**：两者是不同编译器。提交前两个都要跑。等 typescript-eslint 支持 TS 7 后，这套并存可以拆掉，届时删掉 `@typescript/native`、把 `typescript` 换回 `typescript@7` 即可。

## 测试用 Node 内置 runner，没有测试框架依赖

```bash
pnpm test         # node --test "lib/**/*.test.ts"
```

Node 24 直接执行 `.ts`（类型擦除）并自带 `node:test` 与 `node:assert/strict`，因此单元测试**没有引入任何依赖**——这正是根仓库「标准库能解决的不引入依赖」那条约定在前端的落法。约束有两条，都不影响现有代码：

- 被测模块及其依赖里的相对 import **必须写扩展名**（`./errors.ts`），因为 Node 的 ESM 解析不做扩展名补全。为此 `tsconfig.json` 开了 `allowImportingTsExtensions`，Turbopack 与 `next build` 都能正常解析。`@/` 别名 Node 解析不了，所以 `lib/console/` 内部一律用带扩展名的相对路径；`app/` 与 `components/` 不被 Node 直接执行，继续用别名。
- 类型擦除不支持 `enum`、`namespace` 与构造函数参数属性，写测试涉及的模块时避开这三样。

默认 runner 测纯逻辑：契约解析、会话密封、转发白名单、来源校验、请求层的重试与错误分类。P10/Q03 另有独立 `pnpm test:e2e`，使用开发依赖 Playwright 在生产构建与隔离回环服务上验证真实浏览器的布局、键盘、焦点和堆内存；不接真实控制面，不属于默认 `pnpm test`。浏览器驱动无法由 Node 内置测试 runner 提供，因此引入这一个专用开发依赖。复现与覆盖边界见 [P10 验收手册](../../deploy/p10-acceptance.md)。

测试不依赖真实控制面、网络或时钟：`api-client` 的 `fetch` 与 `sleep` 通过参数注入，会话过期用注入的时间判断。

## 提交前门禁

```bash
pnpm lint         # ESLint（内部 TS 6）
pnpm typecheck    # TS 7
pnpm test         # Node 内置 runner
pnpm build        # Next.js 构建，内部 TS 6
```

`pnpm typecheck` 需要 `.next/types` 已由一次 `next build` 或 `next dev` 生成；新增路由后先跑 `pnpm build` 再跑 `pnpm typecheck`。

## 与后端的边界

Console 只调用控制面的 Admin API，不直连 Gateway 数据面、不直连 Agent。根仓库 [AGENTS.md](../../AGENTS.md) 的安全红线同样适用：API Key 明文、完整 Prompt、工作流 JSON 不得进日志或错误提示，前端展示 API Key 一律用后端返回的展示形式（`common/apikey` 定义），不要自己拼。

**唯一的例外：实时 Job 与历史详情页面的取消与产物访问，Console 服务端直连 Gateway 数据面。** 取消（`POST /v1/jobs/{id}/cancel`）与产物下载（`GET /v1/jobs/{id}/artifacts`、`GET /v1/artifacts/{id}`）在 Gateway 数据面认的是租户自己的 API Key，控制面从不持有、也不该持有它，因此这两个操作没有办法走"经控制面转发"这条唯一路径——详细取舍见 [ControlPlane README「Job 持久化契约」](../aiServeWeaveControlPlane/README.md#已实现的持久化历史查询与-console-接入j07)。做法是：owner/admin 用已有的"创建 API Key"功能生成一把该租户的 Key，粘贴进 Console 一个专门的设置页，由 Console 服务端加密存储，仅用于代表已登录会话调用上述取消、产物列举与下载端点；这把 Key **不做 scope 收紧**，与用户自己创建的 Key 权限完全相同，被拿到后不只能取消/读产物，也能拿去跑推理烧配额——这是当前没有真实租户在生产环境运行阶段的刻意权衡，不是遗漏，真有生产租户后应重新评估是否收紧。除这些端点外，其余一切仍然只走控制面；这把 Key **不向浏览器 JavaScript 返回明文**；设置时由用户输入，随后只以密封的 HttpOnly 会话 Cookie 保存，服务端解密使用。

### ComfyUI 编辑器：同源微前端（已接受的风险）

```text
浏览器（wujie 子应用）→ /api/operator/comfy-editor/*（Console 服务端）→ ControlPlane /operator/v1/comfy-editor/*
```

- 这是 Console 里**唯一一条按前缀转发**、而不是按白名单逐行列举的路由：发布包有几百个带哈希的文件名。收窄它的是另外几件事——上游钉死在控制面的编辑器前缀上（`lib/console/comfy-editor.ts` 的 `resolveEditorUpstream` 逐段重新编码并拒绝 `.`/`..`），只附加平台会话，只转发 `If-Modified-Since`/`Range` 两个请求头与一张固定的响应头列表，写请求体在 Console 有界读取后**直接丢弃**、不带往上游，响应体以流转交、上游超时只覆盖到收到响应头为止（几兆的打包文件送往慢速浏览器不能被请求超时截断）（`lib/server/control-plane.ts` 的 `streamControlPlane`，它与 `callControlPlane` 一样是仅有的连接控制面的地方）。新增其他端点不要套用这条路由的做法。
- **同源是一项经评估后接受的风险，不是遗漏。** wujie 把子应用放在同源 iframe 里，ComfyUI_frontend 的代码因此能以运维身份发出同源请求，并能经 `window.parent` 触及宿主页面。之所以接受：前端版本固定，源码归档、受控补丁、语言资源和逐文件产物均校验 SHA-256（`scripts/fetch-comfyui-frontend.sh --localized`、`scripts/comfyui-localization/manifest.json`）。控制面拒绝缺少八语言清单、配置版本不符或文件摘要不符的已配置资源目录；升级服务前先替换对应制品。控制面的 `/api/extensions` 只列构建自带的核心扩展与内嵌桥接，**永远不提供自定义节点的 JavaScript**。升级前端版本或本地补丁时把它当作一次依赖升级来审查。
- 子应用的 `fetch` 由 wujie 替换为宿主提供的版本，只放行 `/api/operator/comfy-editor/` 下的同源请求与 `data:`/`blob:` URL（`isEditorRequestAllowed`）；这是纵深防御，拦得住前端自己对注册表、遥测、模型下载站的外连（实测拦下过对 huggingface 的请求），但它**不是边界**——XHR 与 `window.parent` 都绕得过。WebSocket 被换成只汇报「队列空闲」的本地替身（`FAKE_WEBSOCKET_SCRIPT`），编辑器不会连出任何 socket。
- 跨边界只传字符串（控制面 `internal/comfyeditor/bridge.js`）。不要改成直接传对象：宿主对象属于另一个 realm，ComfyUI 用 `instanceof Array` 判断连线，按引用传进去的图会丢掉全部连线。编辑器的导出先经 `parseEditorExport` 校验并剥离画布视口 `extra.ds`（否则每次滚动后的导出都是新摘要、新版本）；`missing` 非空（图里有目录外节点）时拒绝写入草稿。
- 语言通过 `setLocale` 字符串桥接与图操作串行执行，等待真实语言资源就绪后才报告 ready；不因语言变化重挂载或重载图。受控前端分别维护默认持久化标题、明确作者标题与显示标题，来源标记只在运行时使用。启动脚本对齐 `Response` 与宿主受限 fetch 的 realm，使 WebAssembly 加载器可以继续流式读取，不扩大网络允许列表。
- 草稿里的布局（`workflow`）只在它与文本框里的图文本一致时随发布提交；文本框被手动改过后布局不再对应那张图，发布时省略它，页面给出提示。

### 管理请求使用统一的受限入口

```text
浏览器 → /api/session 或 /api/admin/*（Console 服务端） → ControlPlane Admin API
```

- 浏览器**永远不持有控制面令牌**。租户令牌在 [app/api/session/route.ts](app/api/session/route.ts) 取得，平台令牌在 `app/api/operator-session/route.ts` 取得，密封进 HttpOnly Cookie，由 [lib/server/control-plane.ts](lib/server/control-plane.ts) 在服务端附加到上游请求。新增页面不要直接 `fetch` 控制面地址。
- `/api/admin/*` 不是代理：能转发什么由 [lib/console/upstream-routes.ts](lib/console/upstream-routes.ts) 的白名单决定，含方法、路径与允许透传的查询参数。**新增一个 Admin API 调用 = 往那张表加一行并补测试**，不加就是 404。
- 写操作走 [lib/console/request-origin.ts](lib/console/request-origin.ts) 的同源校验（CSRF 防护是 SameSite=Lax + Origin 检查，不是 token）。部署时反向代理必须原样透传 `Host`。
- 会话 Cookie 由 [lib/console/session-payload.ts](lib/console/session-payload.ts) 用 AES-256-GCM 密封。**不要从浏览器可改的值里读角色**：Admin API 授权由控制面执行；运维入口由服务端运维名单限制，Gateway 例外入口还须检查可信会话/角色，并由 Gateway 校验租户 Key。
- 响应契约在 [lib/console/contract.ts](lib/console/contract.ts) 里对着 `internal/types/types.go` 手写校验，控制面改字段时同步改这里并补测试。上游错误文本一律不渲染，界面文案由 [lib/console/errors.ts](lib/console/errors.ts) 按状态码固定。
- 读请求可重试（上限 3 次），**写请求绝不自动重试**：重发一次创建 Key 会铸出第二个凭据。
- 服务端读取请求体一律走 `lib/server/responses.ts` 的 `readBoundedText`，它**在读取过程中**计数并在超限时取消流。不要改用 `request.text()` 再判断长度：那样上限只是一份读完之后的报告，不带 `Content-Length` 的分块请求可以先把任意大小塞进内存。
- 工作流菜单与运行是**租户**页面（`/api/admin/*`），机群与发布状态是**运维**页面。租户菜单在 `/console/workflows`，副本发布状态在独立的 `/operator/workflows`；租户页面不发起平台请求。
- 运维页面位于 `/operator/*`，由独立平台会话守卫，入口 `/api/operator/*` 仅读取 `aisw_operator_session` Cookie，使用平台 JWT 转发。白名单仍在 `OPERATOR_ROUTES`，客户端必须指定 `surface: "operator"`；不再使用共享运维 token 或邮箱名单。平台与租户 Cookie 的名称、载荷和加密用途隔离，任一退出/401 只清除对应 Cookie。新增节点写操作须同源校验、有界请求体，禁止自动重试。
- P05 后退出必须先调用对应 ControlPlane 会话撤销端点，收到 `204` 或 `401` 才清除本地 Cookie；`503`/超时/断网时保留 Cookie 供重试。租户用户生命周期只走 Admin 白名单，平台运维账户生命周期只走 Operator 白名单，不能交叉复用。
- 三个列表端点返回 `{items, next_cursor}` 信封，分页是 keyset 游标，接口**没有总数**。游标只放组件状态，URL 里只同步筛选条件（`components/console/use-url-filters.ts`），且不放能标识个人的输入。翻页历史有上界（`lib/console/paging.ts`），一页替换上一页而不是追加。

平台会话实现位于 `lib/console/operator-session.ts` 与 `lib/server/operator-session.ts`，AES-256-GCM 使用独立 HKDF 用途与认证版本。平台审计 `/operator/v1/audit` 与租户审计使用不同会话范围。首个平台账户由部署管理员经 BootstrapToken 引导创建；登录后的平台运维可在 `/operator/operators` 创建和管理其他运维，Console 始终不持有 BootstrapToken。

路由管理（P02）位于 `/operator/routes`，草稿只在页面保留。平台发布/回滚须携带 expected_revision，不自动重试；读请求须防止过期响应覆盖新的草稿或生效状态。仅匹配路由验证/发布的请求体允许 1 MiB + 64 KiB，不能全局放宽普通管理端点。后端公共校验源是 `common/modelroute`，前端镜像需同步测试。
