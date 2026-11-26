# Studio：对话与 LLM 编排

Status: ready-for-agent

## Problem Statement

租户成员目前只能通过两种方式使用平台上 Agent 提供的 AI 能力：一是拿 API Key 自己接客户端（Claude Code、Codex、OpenAI SDK 等），二是在 Console 里提交已发布的 ComfyUI 工作流模板。没有一个面向人的、开箱即用的界面，可以直接和平台上的模型对话、上传图片或文件提问、回看历史对话。也没有办法把多次模型调用、条件判断、生图、ComfyUI 模板串成一条可重复运行的流程。

Console 的定位是管理后台（用户、Key、配额、计费、机群）。把对话和编排塞进 Console 会混淆管理与使用两类人群，也会让 Console「只经控制面」的边界变得更复杂。Gateway 目前只认 API Key：普通成员要用 AI，必须先让 owner/admin 建 Key 再粘贴到某处，对只想「打开网页就用」的成员太绕。

## Solution

新开一个前端服务 **Studio**，界面参照常见的 AI 助手（左侧边栏有新建、搜索、对话列表、编排列表；主区是输入框、模型选择和附件）。成员用 Console 的同一套租户账号登录，登录后可以：

- **对话**：选择平台上本租户可用的任意模型，包括 Agent 接入的本地模型、Claude Code / Codex 账号和托管供应商。支持流式回复、停止、重新生成、图片与文件附件。对话历史保存在服务端，换设备也能看到。
- **编排（Flow）**：在画布上用节点组装一条 LLM 流程，包括输入、LLM 调用、条件分支、文本模板、图像生成、运行已发布的 ComfyUI 工作流模板和输出。保存后可以带输入运行，实时查看每一步的进度、输出与用量，运行历史可回看。

后端写在 Gateway 里：新增一组面向会话鉴权的 Studio API，复用 Gateway 已有的调度、模型授权、专属节点、配额、限流、计费与请求日志。会话、消息、编排定义与运行记录通过控制面内部 API 持久化到控制面数据库，与 Job、Responses 持久会话的做法一致。Gateway 不直连数据库。

## User Stories

### 登录与身份

1. As a 租户成员, I want 用 Console 的同一套邮箱和密码登录 Studio, so that 我不需要另一套账号。
2. As a 租户成员, I want 登录后不必创建或粘贴 API Key 就能使用模型, so that 不懂 API 的同事也能直接用。
3. As a 租户成员, I want 刷新页面后仍保持登录, so that 不会被频繁踢出。
4. As a 租户成员, I want 点「退出」后会话立即失效, so that 公用电脑上不会留下可用的登录态。
5. As a 租户 owner, I want 禁用某个用户或撤销其会话后，该用户在 Studio 的请求随即被拒绝, so that 离职人员不能继续消耗配额。
6. As a 租户成员, I want 会话过期时自动跳转登录页，登录后回到原页面, so that 不丢失当前位置。
7. As a 租户成员, I want 控制面暂时不可达时看到「服务暂不可用，请重试」，而不是被登出, so that 短暂故障不会让我重新登录。
8. As a 平台运维, I want 平台运维账号不能登录 Studio, so that 运维身份不会被用来以某个租户的名义消耗资源。
9. As a 安全审计者, I want 会话令牌只存在密封的 HttpOnly Cookie 和服务端之间, so that 浏览器脚本读不到它。

### 模型选择

10. As a 租户成员, I want 在输入框下方看到本租户可用的模型列表, so that 我能选择合适的模型。
11. As a 租户成员, I want 列表只显示我有权调用的模型（受模型授权与专属节点约束）, so that 不会选中一个必然失败的模型。
12. As a 租户成员, I want 看到本租户自有节点上的模型, so that 可以用我们自己接入的算力。
13. As a 租户成员, I want 每个对话记住上次使用的模型, so that 继续对话时不用重选。
14. As a 租户成员, I want 新对话默认使用我上次选择的模型, so that 减少重复操作。
15. As a 租户成员, I want 所选模型下线时看到明确提示并能改选, so that 不会卡在一个发不出去的对话里。
16. As a 租户成员, I want 能区分模型是否支持图片输入, so that 我知道附件会不会被接受。

### 对话

17. As a 租户成员, I want 点「新建」开始一个空对话, so that 可以开始新话题。
18. As a 租户成员, I want 回复按流式逐字显示, so that 不用等完整生成。
19. As a 租户成员, I want 回复按 Markdown 渲染，包括代码块高亮、表格和数学公式, so that 结构化内容易读。
20. As a 租户成员, I want 一键复制代码块或整条回复, so that 方便粘贴到别处。
21. As a 租户成员, I want 生成过程中能点「停止」, so that 回复跑偏时不浪费配额。
22. As a 租户成员, I want 停止后保留已生成的部分并标记为「已中断」, so that 有用的内容不丢。
23. As a 租户成员, I want 对最后一条回复点「重新生成」, so that 不满意时能换一个结果。
24. As a 租户成员, I want 编辑最后一条我的消息并重发，替换其后的回复, so that 修正提问不用新开对话。
25. As a 租户成员, I want 模型的思考内容折叠显示, so that 想看推理过程时可以展开，平时不占地方。
26. As a 租户成员, I want 为对话设置系统提示词, so that 模型按我需要的角色回答。
27. As a 租户成员, I want 调整温度和最大输出长度, so that 控制回复风格与长度。
28. As a 租户成员, I want 粘贴或拖入图片作为附件, so that 可以让模型看图回答。
29. As a 租户成员, I want 上传 PDF 或文本文件作为附件, so that 可以让模型阅读文档。
30. As a 租户成员, I want 附件超过大小或类型限制时，发送前就得到提示, so that 不会白等一次失败的请求。
31. As a 租户成员, I want 对话自动生成一个简短标题, so that 在侧边栏中容易辨认。
32. As a 租户成员, I want 手动重命名对话, so that 标题符合我的习惯。
33. As a 租户成员, I want 删除对话, so that 清理不需要的历史。
34. As a 租户成员, I want 在侧边栏按时间倒序看到我的对话，并能滚动加载更早的, so that 历史很多时也能找到。
35. As a 租户成员, I want 按标题搜索我的对话, so that 快速找到之前的讨论。
36. As a 租户成员, I want 在另一台设备登录后看到同样的对话历史, so that 工作可以接续。
37. As a 租户成员, I want 只看到我自己的对话，看不到同租户其他成员的, so that 隐私得到保护。
38. As a 租户成员, I want 余额不足时看到「余额不足，请联系管理员充值」, so that 我知道为什么失败。
39. As a 租户成员, I want 被限流或节点繁忙时看到「请稍后重试」并可一键重试, so that 临时拥堵不会让我手足无措。
40. As a 租户成员, I want 每条回复显示所用模型和 token 用量, so that 我了解消耗。
41. As a 租户成员, I want 网络中断后重新打开对话时，未完成的回复显示为「已中断」而不是一直转圈, so that 状态如实。
42. As a 租户成员, I want 使用 Claude Code / Codex 账号类模型对话, so that 能用上平台接入的 CLI 账号能力。
43. As a 租户成员, I want 长对话超出模型上下文时得到明确提示, so that 我知道应该新开对话。

### 编排定义

44. As a 租户成员, I want 在侧边栏进入「编排」，看到我的编排列表, so that 管理我的流程。
45. As a 租户成员, I want 新建一个编排并在画布上拖放节点、连线, so that 直观地组装流程。
46. As a 租户成员, I want 用「输入」节点声明运行时输入变量（名称、类型：文本 / 数字 / 布尔 / 图片 / 文件、是否必填、默认值）, so that 每次运行可以传不同的参数。
47. As a 租户成员, I want 用「LLM」节点选择模型，并以模板形式写系统提示词与用户提示词，引用上游变量, so that 模型调用可以用上前面步骤的结果。
48. As a 租户成员, I want LLM 节点可以要求 JSON 结构化输出并声明字段, so that 下游节点可以按字段取值。
49. As a 租户成员, I want 用「条件」节点按上游输出（等于、包含、数值比较、为空）走不同分支, so that 流程可以分情况处理。
50. As a 租户成员, I want 用「文本模板」节点拼接多个变量, so that 不必为了拼字符串额外调用一次模型。
51. As a 租户成员, I want 用「图像生成」节点调用平台上的生图模型, so that 流程可以产出图片。
52. As a 租户成员, I want 用「ComfyUI 模板」节点选择本租户可见的已发布工作流模板、把变量绑定到它的输入, so that 能复用运维发布好的生成管线。
53. As a 租户成员, I want 用「输出」节点声明本次运行的结果字段, so that 运行结束后清楚地看到最终产出。
54. As a 租户成员, I want 多个没有依赖关系的分支并行执行, so that 运行更快。
55. As a 租户成员, I want 保存时立即看到校验错误（有环、未连接的必填输入、引用不存在的变量、模型名为空等），并定位到出错节点, so that 能马上修正。
56. As a 租户成员, I want 编排保存后自动生成新版本，运行记录指向当时的版本, so that 改了编排也能看懂旧的运行结果。
57. As a 租户成员, I want 重命名、复制、删除编排, so that 管理方便。
58. As a 租户成员, I want 导出编排为 JSON 并在别处导入, so that 可以备份或迁移。
59. As a 租户成员, I want 编排里引用的模型或模板后来不可用时，在编辑器中被标出, so that 我知道需要修改哪里。

### 编排运行

60. As a 租户成员, I want 在编排页点「运行」并填写输入表单, so that 用具体参数执行流程。
61. As a 租户成员, I want 运行时在画布上看到每个节点的状态（等待、运行中、成功、失败、跳过）, so that 了解进度。
62. As a 租户成员, I want 点开某个节点查看它的输入、输出、耗时和 token 用量, so that 调试流程。
63. As a 租户成员, I want LLM 节点的输出在运行中流式显示, so that 不用等节点结束。
64. As a 租户成员, I want 取消一次正在进行的运行, so that 停止不需要的消耗。
65. As a 租户成员, I want 某个节点失败时，运行整体标记为失败，并显示失败节点与原因, so that 快速定位问题。
66. As a 租户成员, I want 关闭页面后运行继续，再打开时能看到最新状态, so that 长流程不必一直开着页面。
67. As a 租户成员, I want 查看某个编排的运行历史（时间、状态、耗时、总用量）, so that 回顾执行情况。
68. As a 租户成员, I want 下载运行产出的图片与文件, so that 可以在别处使用。
69. As a 租户成员, I want 用某次历史运行的输入一键重跑, so that 方便复现。
70. As a 租户成员, I want 运行在 Gateway 副本重启后显示为「已中断」，而不是永远停在「运行中」, so that 状态如实。
71. As a 租户成员, I want 超出单次运行的节点数、时长或并发上限时得到明确错误, so that 我知道为什么被拒绝。

### 运维、租户管理与计费

72. As a 租户 owner, I want Studio 产生的用量计入本租户账本，并归属到发起的用户, so that 能在 Console 的用量页按用户查看。
73. As a 租户 owner, I want Studio 的请求受同一套租户配额与限流约束, so that 不会因为换了入口就绕过限额。
74. As a 租户 owner, I want 在 Console 的请求检索中看到来自 Studio 的请求（标为 Studio 来源）, so that 排查问题。
75. As a 平台运维, I want 通过开关整体开启或关闭 Gateway 的 Studio API, so that 未就绪的部署不会暴露它。
76. As a 平台运维, I want Studio API 只接受会话令牌、/v1 只接受 API Key，两者互不通用, so that 一种凭据泄露不会扩散到另一面。
77. As a 平台运维, I want 有 Studio 对话、编排运行数量与失败率的指标, so that 掌握使用情况。
78. As a 平台运维, I want 对话与运行记录有保留期并自动清理, so that 数据库不会无限增长。
79. As a 平台运维, I want 每个用户的对话数、单对话消息数、编排数有上限, so that 单个用户不会撑爆存储。
80. As a 安全审计者, I want 对话内容、提示词和附件不进入任何日志或错误文本, so that 敏感内容不外泄。
81. As a 租户成员, I want 在 Console 顶部看到「打开 Studio」入口, so that 两个应用之间容易跳转。

## Implementation Decisions

### 服务划分

- **新服务 Studio**：独立的 Next.js 服务，与 Console 同栈（Next.js 16 App Router、React 19、Tailwind CSS 4、pnpm、Node 内置 `node:test`），单独构建和部署，有自己的 Dockerfile。画布使用 Console 已在用的 `@xyflow/react`。
- **BFF 形状沿用 Console**：浏览器 → Studio 同源服务端入口 → 上游。服务端固定上游地址，并按「方法 + 路径 + 查询参数」白名单转发，不做任意代理。写操作做同源校验。所有响应 `no-store`，错误体用固定文案，不透传上游文本。
- **登录与登出走控制面，其余全部走 Gateway**：Studio 服务端调用控制面已有的 `POST /admin/v1/auth/login` 和 `DELETE /admin/v1/auth/session` 建立和撤销会话。拿到的会话令牌用 AES-256-GCM 密封进 HttpOnly Cookie，做法与 Console 的会话载荷相同，但使用独立的 HKDF info 和 Cookie 名，两边的 Cookie 互不通用。除登录、登出外，Studio 服务端只调用 Gateway 的 Studio API，不调用控制面 Admin API。
- **共享前端代码**：会话密封、同源校验、白名单匹配等逻辑先在 Studio 内复制一份小而稳定的实现，不在本期抽成跨服务的 npm 包。
- **前端依赖**：
  - 沿用 Console 已有的包，版本对齐：`next`、`react` / `react-dom`、`tailwindcss` 与 `@tailwindcss/postcss`、`tw-animate-css`、`class-variance-authority`、`cn`、`shadcn` + `@base-ui/react`、`lucide-react`、`sonner`、`next-themes`、`@xyflow/react`、`@tanstack/react-virtual`、`@tanstack/react-table`。开发依赖也一样：TypeScript 6/7 并存、`eslint` + `eslint-config-next`、`@playwright/test`。
  - 新增：
    - Vercel AI SDK：用 npm `latest` 标签对应的最新大版本，以 2026-11-16 查询为准：
      - `ai` 7.0.173；
      - `@ai-sdk/react` 4.0.177（它依赖 `ai` 7.0.173，两者须同步升级）；
      - 两者都是 Apache-2.0 许可证。
      - `ai` 有 peer 依赖 `zod`（`^3.25.76 || ^4.1.8`），Console 没有用它，Studio 新增 `zod` 4。
      - 实施时以当时的 `latest` 为准，锁定确切版本，不用 `^` 范围。
      - 用法见下一条。
    - Markdown 渲染：`react-markdown`、`remark-gfm`。
    - 数学公式：`remark-math`、`rehype-katex`、`katex`。
    - 代码高亮：`shiki`，按语言懒加载。
    - 画布自动排布：`@dagrejs/dagre`，可选。
  - 新增依赖须锁定确切版本，并在引入时核实许可证。不引入状态管理库和表单库。
- **Vercel AI SDK 只用于对话界面，并限定在 Studio 内部**：
  - **不使用的部分**：任何模型供应商包（`@ai-sdk/openai` 等）和 `streamText`。Studio 不直连模型，所有模型调用仍由 Gateway 派发。
  - **客户端**：对话页用 `@ai-sdk/react` 的 `useChat`，通过 `DefaultChatTransport` 指向 Studio 自己的同源路由。`prepareSendMessagesRequest` 只发送最新一条消息和对话 id。transport 请求里的触发类型（新消息还是重新生成）映射成 Gateway 的 `regenerate` 和 `replace_from_message_id`。初始消息来自 `GET /studio/v1/conversations/{id}/messages`，先转换成 `UIMessage` 再交给 `useChat`。服务端是唯一的事实来源，客户端状态只是展示用的副本。
  - **BFF 协议转换**：Studio 服务端路由调用 Gateway 的 `/studio/v1` SSE，用 `createUIMessageStream` 和 `createUIMessageStreamResponse` 把 Gateway 的帧逐帧转换成 AI SDK 的 UI message chunk，对应关系如下：
    - `text_delta` 对应 `text-start`、`text-delta`、`text-end`；
    - `thinking_delta` 对应 `reasoning-*`；
    - `usage` 和模型信息放进消息元数据；
    - `error` 映射成固定文案的错误 chunk。
  - **不在 BFF 落库**：转换层不使用 `onEnd` 保存消息，持久化只在 Gateway 发生。
  - **停止**：`useChat` 的 stop 会中止浏览器到 BFF 的请求，BFF 随即中止上游请求，Gateway 据此把消息记为 `incomplete`。
  - **协议边界**：Gateway 的 `/studio/v1` 帧协议保持自有、与 AI SDK 无关。AI SDK 升级或替换时只影响 Studio 的转换层和对话组件，不影响 Gateway 契约。
  - **编排运行不用 `useChat`**：运行事件是节点级的，没有对应的 AI SDK 模型，由 Studio 自己的 SSE 解析器和 reducer 处理。

### Gateway：Studio API 与会话鉴权

- **新路由组 `/studio/v1/*`**：挂在 Gateway 公开监听器上，由新开关启用（默认关闭）。启用时必须配置 `-control-plane-addr`，否则拒绝启动。
- **会话鉴权中间件**：`/studio/v1/*` 只接受 `Authorization: Bearer <控制面会话令牌>`，不接受 API Key；`/v1/*` 继续只接受 API Key，不接受会话令牌。中间件调用控制面新增的内部接口 `POST /internal/v1/sessions/verify`（凭 `InternalToken`）校验令牌，得到一个与 API Key 校验结果同形的身份：租户、模型授权、节点访问策略、余额状态，另外加上 `user_id`、`role` 和会话 id。平台运维会话一律拒绝。
- **校验缓存与失效**：校验结果按令牌哈希在进程内缓存，最多 5 秒（且不超过 `-key-cache-ttl`），与 key 缓存共用同一张表，吊销 generation 推进时一并清空。登出与会话撤销**目前不推进** generation，因此已登出的令牌在单个副本上最多仍可用 5 秒；若需要即时失效，后续让控制面在会话撤销时也推进 generation。控制面不可达时返回 503，不返回 401，与 API Key 路径的语义一致。（2026-11-16 实现时据此修订）
- **身份挂载复用现有路径**：会话身份经过与 API Key 身份相同的 `WithModelAccess`、`WithNodeAccess`、配额、限流、余额 402 处理，调度器不感知凭据类型。
- **用量与请求日志**：用量记录 `key_id` 为空，直接携带 `user_id`，并新增来源字段 `source=studio`。控制面入账在 `key_id` 为空时信任 Gateway 携带的 `user_id`（经内部 Token 认证）。请求检索新增端点取值 `studio_chat`、`studio_flow`。
- **Studio API 资源（契约草图）**：
  - `GET /studio/v1/me`：当前用户与租户的展示信息。
  - `GET /studio/v1/models`：由 `Scheduler.Models(ctx)` 产出，附带每个模型的能力标记（是否支持图片输入、是否为 CLI 账号类模型）。
  - 对话：`GET/POST /studio/v1/conversations`、`GET/PATCH/DELETE /studio/v1/conversations/{id}`、`GET /studio/v1/conversations/{id}/messages`（游标分页）。
  - 发送消息：`POST /studio/v1/conversations/{id}/messages`，以 SSE 流式返回。帧类型包括 `message_start`、`thinking_delta`、`text_delta`、`usage`、`message_stop`、`error`。请求体可带 `regenerate` 或 `replace_from_message_id` 表达重新生成和编辑重发。
  - 停止：客户端断开 SSE 即视为停止。另提供 `POST /studio/v1/conversations/{id}/stop`，供多标签页场景使用。
  - 编排：`GET/POST /studio/v1/flows`、`GET/PATCH/DELETE /studio/v1/flows/{id}`、`GET /studio/v1/flows/{id}/versions/{version}`。（2026-11-26 实现时改为 `PATCH`：改名与保存新定义都是部分更新，可带 `base_version` 防止覆盖别处的保存。）
  - 运行：`POST /studio/v1/flows/{id}/runs`（202 + `run_id`，支持 multipart 上传文件输入）、`GET /studio/v1/runs/{id}`、`GET /studio/v1/runs/{id}/events`（SSE）、`POST /studio/v1/runs/{id}/cancel`、`GET /studio/v1/flows/{id}/runs`、`GET /studio/v1/runs/{id}/artifacts/{artifact_id}`。
  - 所有资源按 `(tenant_id, user_id)` 隔离。不属于调用者的 id 与不存在的 id 一样返回 404。

### 对话的派发

- **每轮从存储重建完整历史**：Gateway 在派发前从控制面读取该对话的消息（副本本地有界缓存，键含租户与用户），拼成 canonical `runtime.ChatRequest`，走与 `/v1/responses` 相同的内部派发路径。这样 Agent 侧本地模型、托管供应商和 Claude Code / Codex 原生兼容层都能服务 Studio 对话，不新增隧道操作，也不新增调度路径。
- **写入顺序**：用户消息在派发前同步写入控制面，失败返回 503，不派发。助手消息在流结束后写入，状态为 `complete`、`incomplete`（停止或断开）或 `error`。助手消息写入失败时，在副本内做有界重试；最终失败时计入指标并在下次打开对话时如实缺失，不编造内容。同一轮次的写入以 `(conversation_id, turn_seq)` 幂等。
- **附件**：图片和文件按 Gateway 现有多模态内容部件（`image_url`、`file`）以 data URI 内联，大小与类型限制复用上传校验的两层做法（扩展名 + 字节嗅探）。附件字节存入已配置的对象存储，消息里只存引用；没有配置对象存储时，附件只在当轮有效，历史中显示为「附件已不可用」。
- **标题生成**：首轮完成后，异步用同一模型（可配置为固定的轻量模型）生成标题。失败时保留截断的首句作为标题。
- **上下文超限**：由后端或适配器返回的超限错误映射为 Studio 专用的错误码 `context_length_exceeded`。Gateway 不自行截断历史。

### 编排（Flow）

- **术语**：仓库里「工作流」已指 ComfyUI 模板运行。新概念统一称为 **编排（Flow）**，节点称为 **编排节点**，一次执行称为 **编排运行（Flow Run）**。已写入 GLOSSARY（2026-11-25）。
- **定义格式与校验放在 `common`**：`common/flowdef`（2026-11-25 已实现）定义编排 JSON 结构、节点类型、变量引用语法、`Decode` 和 `Validate`。Gateway（运行前）与控制面（保存时）调用同一个 `Validate`，不允许各自判断。这与 `common/workflowtemplate.Validate` 的约定相同。与后者不同，`Validate` 收集全部问题（最多 50 条），每条带所属节点 id、字段路径和稳定的错误码（如 `cycle`、`unknown_reference`、`reference_not_upstream`），供编辑器在画布上定位（用户故事 55）。错误文本只含标识符，不引用提示词、标签或取值。
- **分期**：第一期节点类型为 input、llm、template、condition、output，输入类型为 text、number、boolean。image_generation、comfyui_template 节点与 image、file 输入放到第二期，等 Gateway 能运行时再加入 `flowdef`；在此之前，含这些类型的定义一律被拒绝，而不是存下无法运行的定义。
- **定义形状（契约，以 `common/flowdef` 为准）**：

  ```text
  Definition {                       // id、name、version 由控制面管理，不在定义内
    inputs:  [{name, label?, type: text|number|boolean, required, default?}]
    nodes:   [{id, type, label?, config, position: {x, y}}]
    edges:   [{from: node_id, to: node_id, branch?: "true"|"false"}]
    outputs: [{name, ref: 模板，通常为 "{{node_id.field}}"}]
  }
  恰好一个 input 节点（起点，无入边）和一个 output 节点（终点，无出边），二者不带 config；
    inputs 与 outputs 的声明在编辑器里分别显示在这两个节点上
  其余节点都必须能从 input 节点到达；不能有环
  条件节点的出边必须带 branch，其他边不能带
  llm.config:       {model, system?, prompt, temperature? (0–2), max_tokens? (≥1), json_fields?: [{name, type, description?}]}
                    model 是固定的模型名，不能是模板；输出字段 text，外加 json_fields 声明的字段
  template.config:  {template}                 输出字段 text
  condition.config: {left, op, right?}         输出字段 result
                    op: equals | not_equals | contains | not_contains | gt | gte | lt | lte | empty | not_empty
  ```

- **变量引用**：`{{inputs.<name>}}` / `{{<node_id>.<field>}}`，两侧可有空格，纯替换，无表达式求值、无代码执行。只能引用上游节点（输出声明引用输出节点的上游）。`{{` 后不是「标识符.标识符」形式的内容（如 `{{name}}`、JSON）按原文保留，方便提示词里引用模板语言；`{{a.b.c}}` 报错，不支持嵌套字段。标识符为 `[A-Za-z_][A-Za-z0-9_]*`，最长 64 字节；节点 id 不能是 `inputs`。

- **执行引擎是 Gateway 内的深模块 `flow`**（2026-11-25 已实现，尚未接入 HTTP）：对外只暴露「给定定义 + 输入 + 派发器 → 事件流 + 最终结果」。引擎负责拓扑排序、并行分支、条件跳过、变量替换、节点超时、整体期限和取消传播。
  - **接口**：`Engine.Plan(定义, 输入)` 同步校验定义并绑定输入，出错返回 `*flowdef.ValidationError`，HTTP 层据此回 400；`Plan.Run(ctx, emit)` 执行并返回 `Result`，事件按顺序、只从一个 goroutine 发出。
  - **派发器**：第一期只有一个方法 `Chat(ctx, call, delta) (reply, error)`，同步返回整条回复，文字增量经回调送出，失败返回带稳定错误码的 `*flow.Error`。Gateway 侧沿用 Studio 发送消息的做法，进程内调用 `chatCompletions` 并用 tap 取增量。第二期再加入生图与 ComfyUI Job 的方法。
  - **汇合与跳过**：指向某节点的边全部落定后判定该节点，只要有一条激活的入边就运行（「任一分支到达即运行」）。没有激活的入边就跳过，跳过沿分支向下传播。引用被跳过的节点时得到空字符串。输出节点被跳过时，运行仍算成功，只是没有输出。
  - **并行**：互不依赖的节点并行执行，同一次运行最多同时进行 4 个模型调用（可配置）。
  - **失败**：第一个节点失败就让整次运行失败，进行中的模型调用被取消（节点状态 `canceled`），之后不再启动新节点。节点状态有 `succeeded`、`failed`、`canceled`、`skipped`；运行状态有 `succeeded`、`failed`、`canceled`，`interrupted` 由租约扫描器标记。
  - **错误码**：引擎自己的有 `node_timeout`、`run_timeout`、`invalid_json_output`、`not_a_number`、`value_too_large`；派发器的错误码（如 `model_unavailable`、`rate_limited`）原样透传，其他错误一律为 `upstream_error`。
  - **结构化输出**：靠在系统提示词末尾追加字段说明来请求，不用 `response_format`，因为 Claude Code、Codex 账号等许多模型不支持后者。解析时取回复里最外层的 `{...}`，字段值写成字符串也接受（如 `"8"`、`"TRUE"`）。
  - **条件比较**：两侧先去掉首尾空白；数值比较遇到不是数字的一侧，节点以 `not_a_number` 失败。
  - **上限**：单个取值和每次模板渲染最多 1 MiB（可配置），与 Studio 对话的回复上限一致；超长回复截断，渲染超限则节点失败。
- **运行在接收请求的副本上执行**：运行记录写入控制面时带上副本 id 和租约，执行期间定期续租。不做断点续跑，不跨副本迁移。
  - **租约过期（2026-11-25 修订，替代原定的后台扫描器）**：控制面按自己的时钟计算租约到期时间。状态仍为 `running` 但租约已过的运行，读出来就是 `interrupted`，结束时间取租约到期时间，统计进行中运行数时也不计入。该副本之后的续租、步骤和终态写入一律回 409 `studio_run_finished`，Gateway 收到后取消本地执行。这样不需要扫描器，也不改写数据库里那一行。
  - **重试**：运行 id 由 Gateway 铸造，登记运行可安全重试；步骤按 (运行, 节点) 幂等；重复写入同一终态视为成功。
- **步骤结果持久化**：每个节点结束时，把状态、截断后的输出（上限可配置）、耗时和用量写入控制面。这条写入是旁路，不阻塞执行；写入失败时有界重试并计入指标。运行结束时的终态写入必须重试直到成功或租约过期。
- **ComfyUI 模板节点**：通过现有工作流 Job 路径提交，Job 归属同一租户，并受模板 `VisibleTenantIDs` 约束。产物经现有产物 id 与下载路径暴露，运行记录只保存公开的 `artifact_id`。
- **上限**：单个编排最多 50 个节点；单次运行有总期限（默认 30 分钟）和单节点期限（默认 5 分钟）；每个用户同时运行的编排数和每个租户的并发运行数都有上限。超限分别返回 400 或 429。
- **SSE 事件**（2026-11-26 实现时修订）：流以 `run_snapshot` 开头（运行、已结束的步骤、进行中节点迄今的文本），之后是 `node_started`、`node_delta`（LLM 流式输出）、`node_finished`、`node_skipped`、`run_finished`。不再单独发 `run_started`：快照已包含它。断线重连时直接取新快照，不必先读控制面再订阅，也就不会在两者之间漏掉事件。已结束的运行直接回快照与终态，与 Job 事件流的约定一致。如果运行不在本副本，只发快照，客户端改为轮询 `GET /studio/v1/runs/{id}`。跟不上的观察者被断开，重连即可。
- **每次模型调用单独计量**（2026-11-26 实现时补充）：运行中的每次模型调用都单独经过租户限流（各自的配额租约与 token 计数），并使用新的 request id。用量账本按 request id 去重，如果共用启动请求的 id，同一次运行的多次调用只会记一次账。
- **身份**：运行沿用启动请求时的会话身份。会话在运行中途登出或吊销，以及余额在中途耗尽，都不会中止已开始的运行；只有启动运行时做 402 判断。

### 控制面

- **新内部接口**：会话校验 `POST /internal/v1/sessions/verify`；Studio 数据的增删改查内部 API，供 Gateway 使用，全部以 `(tenant_id, user_id)` 为必填范围。编排与运行的接口（2026-11-25 已实现）：`/internal/v1/studio/flows`（`POST`、`GET`）、`/flows/:id`（`GET`、`PATCH`、`DELETE`）、`/flows/:id/versions/:version`、`/flows/:id/runs`、`/internal/v1/studio/runs`（`POST`）、`/runs/:id`（`GET`）、`/runs/:id/lease`、`/runs/:id/steps`、`/runs/:id/finish`，详见控制面 STATUS。保存时定义不合法回 400 `invalid_flow` 并附问题列表；`PATCH` 可带 `base_version`，编排已在别处保存时回 409 `studio_flow_changed`；超出同时进行的运行数上限回 429 `studio_run_limit`。
- **新表**：`studio_conversations`、`studio_messages`、`studio_flows`、`studio_flow_versions`（不可变）、`studio_flow_runs`、`studio_flow_run_steps`。MySQL 9.7 与 PostgreSQL 表结构一致，`sql/init.sql` 同步。`studio_messages` 与 `studio_flow_run_steps` 按时间分区，接入现有分区维护与保留期清理，并新增保留期配置（尚未实现：四张编排表已建，暂不分区，与对话表一样留待保留期任务）。
- **会话撤销联动**：登出、撤销会话、禁用用户时推进吊销 generation，使 Gateway 的会话校验缓存失效。
- **用量入账**：接受 `key_id` 为空且带 `user_id` 的用量记录，以及新的 `source` 字段。升级顺序：**先升级控制面，再升级 Gateway**。

### Console 的小改动

- 顶部栏新增「打开 Studio」链接，地址来自环境变量，未配置时不显示。
- 请求检索与用量页能识别 `studio` 来源。不新增其他页面。

## Testing Decisions

- **好测试的标准**：只通过公开 HTTP 契约断言外部行为，包括状态码、SSE 帧序列、持久化结果和租户隔离；不断言内部函数调用顺序或私有结构。
- **主 seam：Gateway HTTP 层**。沿用 `httpapi` 现有测试的写法：`httptest` 起 Gateway 处理器，注入假调度器和假控制面客户端，覆盖：
  - 会话鉴权：有效、无效、平台运维、控制面不可达返回 503、吊销 generation 推进后缓存失效、API Key 打 `/studio/v1` 被拒、会话令牌打 `/v1` 被拒。
  - 模型授权与专属节点对 Studio 请求同样生效。
  - 对话：SSE 帧序列；停止与断开时写入 `incomplete`；重新生成与编辑重发；用户消息写入失败时不派发；跨用户与跨租户返回 404；余额耗尽返回 402；限流返回 429。
  - 编排：保存时的校验错误、并行与条件分支、取消、节点失败、上限、租约过期后被标为 `interrupted`、SSE 重连。
  - 用量记录携带 `user_id` 和 `source`。
  - 先例：`auth_test.go`、`messages_auth_test.go`、`jobs_test.go`、`jobevents_test.go`、`jobpersist_integration_test.go`、`jobrecover_integration_test.go`、`requestlog_test.go`。
- **编排执行引擎**：作为深模块单独测，用假派发器驱动，覆盖拓扑、并行、条件跳过、变量替换、期限与取消。它是 HTTP seam 之下唯一新增的测试 seam，因为引擎的分支组合用 HTTP 测太贵。
- **`common/flowdef`**：表驱动测试 `Validate`。先例：`common/workflowtemplate` 的校验测试。
- **控制面**：新表与内部 API 走 gormstore live 用例（临时 MySQL 9.7 与 PostgreSQL 16），覆盖租户与用户隔离、幂等写入、分区与保留期清理、会话校验与撤销联动。先例：现有 gormstore live 用例与 `response_turns`、`jobs` 的契约测试。
- **Studio 前端**：纯逻辑用 `node:test`，覆盖会话密封、同源校验、白名单、SSE 帧解析、编排定义与画布模型的互转，以及 Gateway 帧到 AI SDK UI message chunk 的转换（帧序列进、chunk 序列出，包括思考、错误、中途断开和 Gateway 消息到 `UIMessage` 的转换）。主流程用 Playwright e2e 对接桩 Gateway，包括登录、对话流式与停止、编排保存与运行。先例：Console 的 `lib/console/*.test.ts` 与 `e2e/`。

## Out of Scope

- 独立的 C 端用户注册体系；Studio 只服务租户成员。
- 对话分支树（编辑任意历史消息并保留多个分支）。v1 只支持替换最后一轮之后的内容。
- 编排在租户内共享、发布为模板、对外以 API 暴露或定时触发。v1 的编排是用户私有的。
- 在对话中调用编排，或让模型以工具调用的方式使用编排。
- 编排节点：HTTP 请求、任意代码执行、知识库与 RAG（嵌入和 rerank 节点）、循环和迭代、人工审批。
- 服务器端执行客户端工具，以及在 Studio 中运行 Claude Code / Codex 的 agent 工具循环（文件读写、shell）。Studio 只使用它们的对话能力。
- 编排运行的断点续跑与跨副本迁移。
- 抽取 Console 与 Studio 的共享 npm 包。
- 国际化（v1 只做中文界面）。

## Further Notes

- **术语**：「编排」「编排节点」「编排运行」以及与之区分的「工作流」已写入 GLOSSARY.md（2026-11-25）。「Studio」尚未收录。
- **ADR 候选**：「Studio 后端放在 Gateway 而非控制面」与「会话令牌在数据面的鉴权」值得各记一条 ADR。目前仓库没有 `docs/adr/`。
- **安全红线**：对话内容、提示词、编排输入输出和附件不得进入日志、错误文本或指标标签。错误文本统一经 `runtime.Redact`。
- **升级顺序**：先升级控制面（新表、内部接口、用量字段），再升级全部 Gateway，最后开启 Studio API 开关并部署 Studio。
- **交付文档**：落地后同步根 STATUS.md、Gateway STATUS.md、控制面 STATUS.md、新建 Studio 的 README.md 与 STATUS.md，并在 Gateway `openapi.yaml` 中补充 `/studio/v1`。
- **建议拆分**：（1）控制面会话校验与用量字段；（2）Gateway 会话鉴权与 `/studio/v1` 骨架；（3）对话存储与派发；（4）Studio 前端：登录与对话；（5）`common/flowdef` 与执行引擎；（6）编排存储、运行与事件；（7）Studio 前端：编排画布与运行视图；（8）保留期、指标与 Console 入口。
