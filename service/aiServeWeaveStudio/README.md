# aiserveweave-studio

Studio 是面向租户成员的 AI 工作台：与平台上的模型对话，以及（规划中）编排 LLM 流程。规格见 [.scratch/studio-chat-flows/spec.md](../../.scratch/studio-chat-flows/spec.md)，进度见 [STATUS.md](STATUS.md)。

## 调用链路

```text
浏览器 ──同源──► Studio 服务端 ──登录/登出──► 控制面 /admin/v1/auth/*
                     │
                     └──会话令牌──► Gateway /studio/v1/*  ──► 调度器 / Agent
```

- 用户用 Console 的同一套租户账号登录。控制面签发的会话令牌用 AES-256-GCM 密封进 HttpOnly Cookie（`aisw_studio_session`），浏览器读不到。
- 除登录、登出外，Studio 服务端只调用 Gateway 的 Studio API。浏览器经 `/api/studio/*` 访问，只放行白名单内的方法与路径（`lib/studio/upstream-routes.ts`），错误体只保留稳定错误码。
- Gateway 用控制面内部接口 `POST /internal/v1/sessions/verify` 校验会话。会话令牌在 `/v1/*` 上无效，API Key 在 `/studio/v1/*` 上也无效。
- 发送不走白名单转发，而是 `POST /api/studio/conversations/{id}/stream`：转发到 Gateway `POST /studio/v1/conversations/{id}/messages`，把 Gateway 的 SSE 帧翻译成 Vercel AI SDK 的 UI 消息流（`lib/studio/stream-translate.ts`）。对话与消息由 Gateway 存在控制面，Studio 不存储。
- Gateway 拒绝会话（401）时，页面经 `/api/session/expired` 清掉失效的 Cookie 再去登录。

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

- 技术栈与 Console 对齐：Next.js 16 App Router、React 19、Tailwind CSS 4、Base UI + shadcn 组件、`node:test`。对话界面用 Vercel AI SDK（`ai`、`@ai-sdk/react`），只用于前端状态与流，不在 Studio 里直接调用模型。
- 令牌、密码与对话内容不得进入日志、localStorage、URL 或错误文案。localStorage 只存单浏览器偏好（侧边栏折叠、上次选择的模型）。
- 界面文案为中文。上游错误统一经 `errorMessage` 映射为固定文案，不展示上游原文。
