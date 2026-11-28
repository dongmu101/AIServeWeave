## 编排的视频生成节点（2026-11-28）

新增「视频生成」节点（火山方舟）。代码、lint、typecheck、147 项单元测试与生产构建已通过，**未在浏览器里走通，也未接真实火山方舟**。

- 节点面板（`flow-node.tsx`）与配置表单（`node-inspector.tsx` 的 `VideoForm`）：模型从 `GET /studio/v1/video-models` 选择；租户用自己的 Key 时可以自由填写模型或接入点 ID。首帧、尾帧与参考图只能从图片来源下拉中选（`referencesFor(..., "image")`：image 输入、上游的 `image` 与 `last_frame`）。另有分辨率、宽高比、时长、生成音频与水印，均可留给模型默认。
- 即时检查：`image_reference_required`、`video_generation_unavailable`，以及模型不在平台白名单时的 `model_unavailable`。
- 运行视图：视频节点显示「正在生成视频」提示，步骤输入中的图片按图片展示；产物视图对视频内联 `<video controls>` 播放并保留下载。产物下载不支持 Range，Safari 可能无法内联播放。
- 新的上游白名单：`GET video-models`。

## 编排的图片与文件输入（2026-11-27）

编排运行可以带图片与文件（flowdef 输入类型 `image`、`file`）。本节只记 Studio；`common/flowdef` 与 Gateway 一侧见根 STATUS 与 Gateway 代码。本节取代下面「编排第二期节点」里关于文件型模板输入「只显示说明、必填则不能运行」的说法。

- **输入声明**：输入节点的类型下拉新增「图片」「文件」，没有默认值（切换类型时清掉默认值，保存时也不发送）。解析 `image`、`file` 输入时忽略任何默认值。
- **引用规则与即时校验**（`lib/studio/flow-graph.ts`、`artifacts.ts`，对应 flowdef 的 `artifact_reference`）：产物指 image/file 输入、image_generation 与 comfyui_template 的 `image`、comfyui_template 的 `artifacts`。输出 ref 整个引用可以引用任何产物；comfyui_template 的模板输入整个是单个文件（image/file 输入，或某节点的 `image`，不含 `artifacts`）也可以；其余位置（提示词、模板、条件、图像提示词，以及 ComfyUI 输入里与其他文字混写）立刻标出 `artifact_reference`。变量菜单按去处分三种：文本位置不列任何文件类引用；输出的结果字段全部列出；ComfyUI 文件型输入只列文件来源。
- **ComfyUI 文件型模板输入**：编辑器给「选择文件来源」下拉（本编排的图片/文件输入，以及上游产出图片的节点），不给文本框；必填未选、选了文件以外的内容、文本型输入绑了文件，都在节点上标黄并在运行前拦下（运行时 Gateway 报 `invalid_input`）。不再产生 `unsupported_input`；历史运行里的该码保留原文案。
- **运行表单**（编辑器「运行」与运行视图「重跑」共用）：image 输入用 `accept="image/*"` 的文件选择器，file 输入不限类型；显示将上传的文件名与大小；重跑时预填上次的文件（显示文件名，选新文件替换，或「移除」）。前端不设类型与大小限制，以 Gateway 的拒绝为准。服务端的 `required`、`unknown_input`、`invalid_value`、`unsupported_file_type`、`not_found`、`artifact_storage_unavailable` 回填到对应输入下（中文文案）；413 `too_large` 与 503 `storage_unavailable` 在表单底部提示。
- **提交**（`lib/studio/run-inputs.ts`）：不带文件时仍发 JSON `{"inputs":…}`；带文件时发 multipart：`request` 字段放同样的 JSON，每个文件一个以输入名命名的分片（文件不同时出现在 JSON 里）。重跑复用的文件写成 `{"run_id":"<旧运行>","artifact_id":"<旧产物>"}` 放进 `inputs`。
- **BFF**：启动运行改走专门路由 `POST /api/studio/flows/{id}/runs/start`（原白名单里的 `POST flows/{id}/runs` 移除，它会整体缓冲请求体并有 2 MiB 上限）。新路由校验来源、会话与编排 id，把请求体以流转发给 Gateway `POST /studio/v1/flows/{id}/runs`（保留 `Content-Type` 与分隔符、`Content-Length`），不缓冲，不设自己的大小上限；只接受 `application/json` 与 `multipart/form-data`；应答只保留稳定错误码与问题列表（`lib/server/gateway-answer.ts`，与白名单转发共用）。
- **运行视图**：新增「输入文件」区（图片内联预览，其他文件为文件名加下载）；输出 ref 直接引用文件输入时按产物展示（image 输入显示为图片，file 输入显示文件名加下载，文件名取自运行记录的 FileValue）；ComfyUI 节点详情里绑定了单个文件的输入同样按文件展示。下载先探测响应头：文件已过保留期（404）时提示「这个文件已不存在，可能超过保留期被清理了」，而不是存下一份错误页；图片加载失败提示改为「可能已超过保留期被清理」。节点失败码 `not_found`（用到的文件已被清理）与 `invalid_input` 有新文案。

验证：lint、typecheck、147 项单元测试（新增：引用规则含文件输入与 ComfyUI 单文件输入、变量菜单三种去处、模板文件输入的绑定检查、run-inputs 的表单预填/重跑复用/multipart 构造/服务端错误回填、FileValue 解析与输入文件列表、新错误码文案、路由白名单）与生产构建通过。用桩控制面与桩 Gateway 加生产构建（`next start`）做了 HTTP 级检查：登录后经 `runs/start` 上传 60 MiB 与 250 MiB 的 multipart，桩 Gateway 收到的字节数、`Content-Type`（含分隔符）与 `Content-Length` 一致；250 MiB 上传期间 Studio 进程内存只增加约 50 MB，没有整体缓冲；桩 Gateway 的 400 问题列表转发为 `{error, issues}` 且不含其 message；JSON 请求（含复用文件引用）正常；无会话 401、跨站来源 403、非法编排 id 404、`text/plain` 400、旧的 `POST flows/{id}/runs` 404。

未验证：没有在浏览器里走通（本机没有可用的无头浏览器）：输入声明面板、文件选择器与重跑预填的交互、文件来源下拉、输入文件区的预览与下载按钮（含 404 提示）、手机宽度与深色主题；未接真实 Gateway 联调，multipart 的字段约定按任务给出的契约实现；未经真实的 nginx 与 Cloudflare：`deploy/nginx.example.conf` 的 Studio 站点已为 `/api/studio/flows/{id}/runs/start` 单独放宽到 200m 并关闭请求体缓冲（站点其余路径仍为 10m），已部署的 nginx 需照此调整；经 Cloudflare 代理时其套餐上限（免费版 100 MB）先生效。

## 编排第二期节点（2026-11-27）

编排编辑器与运行视图加入「图像生成」与「ComfyUI 模板」两类节点（规格用户故事 51、52、59、68 的前端部分）。本节只记 Studio；`common/flowdef` 与 Gateway 一侧见[根 STATUS](../../STATUS.md#studio-编排第二期节点2026-11-27)。

- **编辑器**：左侧新增「图像生成」「ComfyUI 模板」。
  - 图像生成：模型下拉来自 `GET /studio/v1/image-models`，仅当 `default_available` 为真时才有「部署默认」；提示词是模板（有「插入变量」）；尺寸可选，按 flowdef 的规则（`宽x高`，每边 1–16384、无前导零）即时标红。
  - ComfyUI 模板：模板下拉来自 `GET /studio/v1/workflow-templates`；选中后为模板声明的每个输入生成一个模板输入框，标签写明类型、必填、默认值与长度或数值范围，留空则用模板默认值。文件类型的输入只显示说明，必填的文件输入使该节点不能运行。换模板时保留同名输入，其余丢弃；模板没有的输入显示为可移除的黄色条目。
  - 模型或模板不可用、必填输入未设置、必填文件输入、模板没有的输入，都在节点上标黄，运行前拦下，与已有的 LLM 模型检查并列。点「运行」时会重新读取两份目录，因此页面打开之后被取消发布的模板也能被拦住；目录加载失败时不据此标记任何东西，概览里给出提示。
  - 产物字段（`image`、`artifacts`）只出现在输出节点结果字段的「插入变量」菜单里，其他节点的菜单不列出。手写引用时（`lib/studio/flow-template.ts` 是 flowdef 模板语法的客户端镜像）在提示词、模板、条件、图像提示词、ComfyUI 输入里引用，或在结果字段里与其他文字混写，会立刻标出 `artifact_reference`，不必等保存；保存时服务端报告的同类问题与之合并，不重复。结果字段整个引用产物是允许的，并有说明。
- **运行视图**：产物输出是否为图片或文件，由运行所用版本的定义判断（结果字段的 ref 是对 `image_generation` 或 `comfyui_template` 节点产物字段的唯一引用），不看值的形状。`image` 显示为图片，`artifacts` 显示为列表（图片内联缩略图，视频与其他文件为文件名加下载链接），每项和每张图都有「下载」；点图片可放大。节点详情里同样展示。图片加载失败显示「图片无法加载，可能已被清理」。节点失败原因新增 `no_image_produced`、`image_generation_unavailable`、`template_unavailable`、`invalid_input`、`unsupported_input`、`no_capable_node`、`queue_timeout`、`generation_failed`、`out_of_memory`、`job_cancelled`、`artifact_storage_unavailable` 的中文文案；运行中的图像与 ComfyUI 节点不显示逐字输出，只提示「正在生成」。
- **BFF**：白名单新增 `GET image-models`、`GET workflow-templates`。产物下载走独立路由 `GET /api/studio/runs/{id}/artifacts/{artifactId}`：校验来源、会话与两个 id，对 Gateway 的响应只传递 `Content-Type`、`Content-Length`、`Content-Disposition`，字节不经缓冲直接转发，没有大小上限（最长 15 分钟），并固定加 `X-Content-Type-Options: nosniff`、`Content-Security-Policy: sandbox`、`Cache-Control: private, no-store`；Gateway 没给 `Content-Disposition` 时补 `attachment`，给了 `Content-Encoding` 时不传 `Content-Length`。错误只保留稳定错误码。

验证：lint、typecheck、121 项单元测试（新增模板引用解析、产物值与下载头、两份目录的契约解析、产物字段的引用菜单与即时检查、目录可用性检查、新节点的序列化、白名单）与生产构建通过。用桩控制面与桩 Gateway 加生产构建（`next start`）做了 HTTP 级检查：登录后下载 8 MiB 的桩产物，字节数一致，响应头为上述集合（桩 Gateway 给出的 `Content-Security-Policy: default-src *` 被覆盖为 `sandbox`）；上游 404、非法 id、无会话分别得到 404、404、401；两份目录经白名单转发；运行页与编辑器页服务端渲染成功，运行页的结果里 `image` 渲染为 `<img src="/api/studio/runs/…/artifacts/…">`，`artifacts` 列表为缩略图与下载链接。

未验证：没有在浏览器里走通（本机没有 Playwright 与无头浏览器）：画布上新节点的外观、配置面板的交互、缩略图放大、下载按钮的实际下载；手机宽度与深色主题；未接真实 Gateway、ComfyUI 与对象存储联调，上面的 Gateway 契约是按约定实现的。

## 编排（2026-11-26）

侧边栏「编排」可用了，覆盖规格用户故事 44–69 中第一期范围内的部分。

- **列表**（`/flows`）：新建（自带输入节点 → 输出节点和一个 `topic` 输入，可直接保存）、导入 JSON、重命名、复制、导出 JSON、删除（连同版本与运行记录）。
- **编辑器**（`/flows/{id}`，`@xyflow/react` 12.12.0，MIT）：
  - 左侧节点栏添加 LLM、文本模板、条件、图像生成、ComfyUI 模板节点：点击放在画布中心附近，或拖到画布上放在松手处（桌面浏览器的原生拖放，触屏只能点击）；从节点右侧连接点拖线连接，条件节点有「是」「否」两个连出点；选中节点或连线后按 Delete 删除。连线在客户端先做即时检查（不能连到自己、不能连入输入节点、不能从输出节点连出、不能成环、条件必须选分支）。
  - 配置面板（节点栏与画布之间，窄屏在画布下方）：显示名称；输入节点编辑运行时输入（名称、类型、必填、默认值）；输出节点编辑结果字段；LLM 节点选模型、系统提示词、提示词、温度、最大输出长度和 JSON 结构化字段；文本模板；条件的比较值、比较方式与对比值。所有模板框都有「插入变量」菜单，只列出该节点可引用的输入与上游字段。
  - 保存（⌘S / Ctrl+S）带上 `base_version`；编排在别处保存过时提示刷新。定义不合法时，问题按节点标在画布上（红框和数量），选中节点后列出具体问题。引用了当前不可用模型的节点标黄，运行前拦下。有未保存修改时离开页面会提醒。
- **运行**：运行表单按输入声明生成，客户端先校验必填与数字，服务端的输入问题也回填到对应字段。启动后跳到运行视图（`/flows/{id}/runs/{runId}`）：
  - 画布展示运行所用的版本，节点显示运行中、成功、失败、已取消、已跳过；
  - 右侧显示耗时、用量、结果（可复制）与失败原因，点选节点查看渲染后的输入、输出、模型、用量和耗时，运行中的 LLM 节点实时显示正在生成的文字；
  - 可停止、可重跑（表单预填上次的输入）。
  - 事件流经新的 BFF 路由 `GET /api/studio/runs/{id}/events` 原样转发，浏览器解析（`lib/studio/flow-run.ts`）。流提前结束时自动重连（1–5 秒退避），每次重连都从快照开始，因此运行在其他 Gateway 副本上时也能看到进度。
- **运行记录**（`/flows/{id}/runs`）：时间、状态、版本、耗时、用量，可加载更多。
- **BFF**：白名单新增编排与运行的 10 条路由；保存编排的请求体上限放宽到 1 MiB + 64 KiB，其余仍为 64 KiB（启动运行当时放宽到 2 MiB，现已改走流式的专门路由，见 2026-11-27 的图片与文件输入）。400 应答中的问题列表只转发节点、字段与错误码，不转发上游的说明文字。
- 画布只在客户端水合后渲染：它从浏览器读取配色方案，服务端渲染的猜测会在水合后残留，导致深色主题下画布仍是浅色。

验证：lint、typecheck、76 项单元测试（新增编排契约解析、问题文案、画布模型的连线规则与引用列表、运行事件归约、运行表单与导入导出、白名单）与生产构建通过。用桩控制面与桩 Gateway 加生产构建，在无头 Chrome（Playwright）中走通：登录、空列表、新建、添加 LLM 节点、保存时问题标在节点上、拖线连接、填写提示词与结果字段（自动填入 `{{llm_1.text}}`）、保存为第 2 版、运行表单拦下必填项、运行中节点状态与逐段文字、运行结束的结果与节点详情、重跑预填输入、停止后「已取消」、运行记录、编排列表、手机宽度（无横向滚动）、深色主题。未接真实 Gateway 与控制面联调。

# Studio 开发状态

## 搜索对话（2026-11-25）

侧边栏「搜索」或 ⌘K / Ctrl+K 打开搜索框，按标题查找自己的对话（规格用户故事 35）。查询为空时列出最近对话；输入后 200 ms 防抖，经 `/api/studio/conversations?q=` 交给控制面做不区分大小写的子串匹配（`%`、`_` 按字面匹配），结果按更新时间倒序，每页 20 条，可「加载更多」。匹配部分高亮，右侧显示更新时间（今天显示时刻，昨天显示「昨天」，更早显示日期）。↑↓ 选择、Enter 打开、Esc 关闭；点击结果或按 Enter 跳到 `/c/{id}`。查询截到 200 个字符，与控制面的标题上限一致。会话失效跳转登录，其他失败在框内显示错误文案。窄屏从抽屉打开时先收起抽屉。Gateway、控制面与 BFF 白名单原本已支持 `q`，本次只改前端（`components/studio/conversation-search.tsx`、`lib/studio/conversation-search.ts`）。

只搜标题，不搜消息正文。

验证：lint、typecheck、52 项单元测试（新增查询规整、请求地址、高亮、时间标签）与生产构建通过；用桩上游加生产构建在无头 Chromium 中走通：打开即聚焦、最近对话、搜索与高亮、`50%` 字面匹配、无结果、键盘选择后 Enter 打开、快捷键开关、重开时清空、加载更多直到末页、无标题显示「新对话」、点击打开、上游错误、深浅色主题、手机宽度（抽屉收起、无横向滚动）。未接真实 Gateway 与控制面联调。

## 从 Console 免密码登录（2026-11-17）

`POST /api/session/handoff` 接收 Console 以表单提交的密封票据（`lib/studio/handoff-ticket.ts`，AES-256-GCM，密钥由 `AISW_STUDIO_HANDOFF_SECRET` 派生，60 秒有效），写入 Studio 会话后 303 到首页；此处已有的另一个会话先尽力吊销。来源校验（`lib/studio/handoff-origin.ts`）：设置了 `AISW_STUDIO_CONSOLE_URL` 时只接受其中的来源，否则只接受同站点提交，防止别处页面把访客登进别人的账户。任何失败都 303 到登录页。验证：lint、typecheck、39 项单元测试（新增票据与来源校验）与生产构建通过；未接真实 Console 走通。

规格：[.scratch/studio-chat-flows/spec.md](../../.scratch/studio-chat-flows/spec.md)。

## 对话（2026-11-16）

已交付：

- **发送**：首页发送即新建对话并跳到 `/c/{id}`；对话页经 AI SDK `useChat` 与自定义的 `StudioChatTransport` 流式接收。BFF `POST /api/studio/conversations/{id}/stream` 校验来源、会话与请求体，转发 Gateway，并把 SSE 字节原样转回（自身不存储，最长 15 分钟）。帧在浏览器里翻译成 UI 消息流：中途截断以「已中断」错误结束，用户停止按中止处理。验证（2026-11-24）：9 项新增 Node 测试，覆盖任意分片、截断、中止、取消与拒绝；用桩 Gateway 在无头 Chromium 中验证了逐段到达、停止后「已中断」并中止上游、拒绝时恢复输入。真实 Gateway 尚未联调。请求只带最新一条消息与模型，历史由服务端保存。
- **回答**：Markdown（GFM 表格、代码）、可折叠的思考过程、复制、重新生成最后一条回答、停止（显示并保存为「已中断」）、贴底滚动。
- **错误**：写入前被拒（如模型不可用、达到上限）时把文字放回输入框且不提供重试；生成中出错可重试。会话失效跳转登录。
- **侧边栏**：最近对话列表，支持重命名与删除；删除当前对话回到首页。
- **转发白名单**：新增对话的列表、新建、读取、重命名、删除与消息列表；发送只走上面的流式路由。

与规格的差异：标题取首条消息首行（不调用模型生成）；附件可选但不发送（提示尚未支持）；没有编辑后重发；停止靠断开连接，没有服务端停止接口。

已通过的检查：`pnpm lint`、`pnpm typecheck`、`pnpm test`（33 项，新增 SSE 解析、帧翻译、发送请求体、历史转换、模型选择）、`pnpm build`；用桩上游在无头 Chromium 中走通发送、刷新后历史、重新生成、停止、拒绝、重命名、删除，以及手机宽度。

未验收：接真实控制面、Gateway 与模型的联调。

## 首页与登录（2026-11-16）

已交付：

- **服务骨架**：Next.js 服务（端口 3100）、Dockerfile、`.env.example`。
- **登录与登出**：复用租户账号，会话令牌密封在独立的 Cookie 里。登出时先吊销控制面会话，确认后再清 Cookie。
- **转发入口**：`/api/studio/*` 白名单转发到 Gateway，首页阶段只放行 `GET me` 与 `GET models`。
- **首页**：
  - 侧边栏：新建对话、搜索与编排（当时标为「即将上线」，搜索已于 2026-11-25 交付）、最近对话（空状态）、用户菜单与退出。宽屏可折叠，窄屏为抽屉。
  - 问候语：「嗨，{姓名}」，没有姓名时取邮箱 @ 前的部分。
  - 输入框：自动增高；Enter 发送，Shift+Enter 换行，输入法组合输入时不触发发送。
  - 「+」菜单：上传或添加文件，快捷键 ⌘U / Ctrl+U。也支持拖放与粘贴文件。附件发送前就在客户端校验类型、大小与数量。
  - 模型选择：列出 Gateway 按本租户模型授权给出的模型，上次的选择记在本浏览器。

编排见上文「编排（2026-11-26）」。搜索、发送与对话历史见上文。

已通过的检查：

- `pnpm lint`、`pnpm typecheck`、`pnpm test`（18 项：契约解析、错误文案、白名单、附件校验、模型偏好）、`pnpm build`。
- 用桩上游（模拟控制面登录与 Gateway 模型列表）加生产构建，在无头 Chromium 中走通了以下流程：
  - 未登录访问被重定向到登录页；
  - 错误密码提示；
  - 登录后首页；
  - 「+」菜单；
  - 附件（`.exe` 被拒）；
  - 模型菜单与刷新后保留选择；
  - 手机宽度；
  - 退出登录。

  深色与浅色主题都检查了，页面无脚本错误。

未验收：对接真实控制面与 Gateway 的端到端登录。
