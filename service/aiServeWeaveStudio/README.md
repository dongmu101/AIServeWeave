# aiserveweave-studio

Studio 是面向租户成员的 AI 工作台：与平台上的模型对话，以及在画布上编排、运行 LLM 流程（编排）。规格见 [.scratch/studio-chat-flows/spec.md](../../.scratch/studio-chat-flows/spec.md)，进度见 [STATUS.md](STATUS.md)。

## 调用链路

```text
浏览器 ──同源──► Studio 服务端 ──登录/登出──► 控制面 /admin/v1/auth/*
                     │
                     └──会话令牌──► Gateway /studio/v1/*  ──► 调度器 / Agent
```

- 用户用 Console 的同一套租户账号登录。控制面签发的会话令牌用 AES-256-GCM 密封进 HttpOnly Cookie（`aisw_studio_session`），浏览器读不到。
- 除登录、登出外，Studio 服务端只调用 Gateway 的 Studio API。浏览器经 `/api/studio/*` 访问，只放行白名单内的方法与路径（`lib/studio/upstream-routes.ts`），错误体只保留稳定错误码。
- Gateway 用控制面内部接口 `POST /internal/v1/sessions/verify` 校验会话。会话令牌在 `/v1/*` 上无效，API Key 在 `/studio/v1/*` 上也无效。
- 发送不走白名单转发，而是 `POST /api/studio/conversations/{id}/stream`：鉴权后转发到 Gateway `POST /studio/v1/conversations/{id}/messages`，并把 Gateway 的 SSE 字节原样转回浏览器。翻译成 Vercel AI SDK UI 消息流的工作在浏览器里完成（`lib/studio/chat-transport.ts` → `gateway-stream.ts` → `stream-translate.ts`），服务端不再逐个 token 解析。浏览器收到的只是 Gateway 帧的内容：用户自己的会话与回答，以及固定文案的错误码，不含任何响应头或令牌。对话与消息由 Gateway 存在控制面，Studio 不存储。
- Gateway 拒绝会话（401）时，页面经 `/api/session/expired` 清掉失效的 Cookie 再去登录。
- 编排运行的事件流走 `GET /api/studio/runs/{id}/events`：鉴权后转发 Gateway `GET /studio/v1/runs/{id}/events`，SSE 字节原样转回，由浏览器解析（`lib/studio/flow-run.ts`）。关闭页面只是停止观察，运行仍在 Gateway 上继续。
- 运行产物（生成的图片与文件，以及运行启动时收到的输入文件）走 `GET /api/studio/runs/{id}/artifacts/{artifactId}`：鉴权并校验两个 id 后转发 Gateway `GET /studio/v1/runs/{id}/artifacts/{artifact_id}`，字节不经缓冲、没有大小上限，只传递类型、长度与 `Content-Disposition`，并固定加 `nosniff`、`Content-Security-Policy: sandbox`、`Cache-Control: private, no-store`。图像模型与工作流模板目录（`GET image-models`、`GET workflow-templates`）走白名单转发。
- 启动运行走 `POST /api/studio/flows/{id}/runs/start`：鉴权后把请求体（JSON，或带图片/文件输入时的 multipart/form-data）以流转发给 Gateway `POST /studio/v1/flows/{id}/runs`，保留 `Content-Type` 与长度，不缓冲、不设自己的大小上限（上限以 Gateway 为准，整个请求 200 MiB）。
- 编排与运行的其他请求走同一个白名单转发。保存编排的请求体上限放宽到 1 MiB + 64 KiB。被拒绝时，问题列表只转发节点、字段与错误码，供编辑器在画布上定位。

## 本地运行

前置：控制面已启动；Gateway 以 `-studio -control-plane-addr <控制面地址>` 启动。

```bash
cp .env.example .env.local   # 按需修改
pnpm install
pnpm dev                      # http://127.0.0.1:3100
```

## 门禁

```bash
pnpm lint && pnpm typecheck && pnpm test && pnpm build
```

`pnpm typecheck` 依赖 Next 生成的路由类型（`LayoutProps` 等）。全新克隆时先运行一次 `pnpm build` 或 `pnpm exec next typegen`。

## 约定

- 技术栈与 Console 对齐：Next.js 16 App Router、React 19、Tailwind CSS 4、Base UI + shadcn 组件、`node:test`。编排画布用 `@xyflow/react`（与 Console 同版本 12.12.0），只在客户端水合后渲染。对话界面用 Vercel AI SDK（`ai`、`@ai-sdk/react`），只用于前端状态与流，不在 Studio 里直接调用模型。
- 令牌、密码与对话内容不得进入日志、localStorage、URL 或错误文案。localStorage 只存单浏览器偏好（侧边栏折叠、上次选择的模型）。
- 界面文案为中文。上游错误统一经 `errorMessage` 映射为固定文案，不展示上游原文。
