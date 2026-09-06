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

`cn` 工具函数来自 shadcn 官方的 [`cn`](https://github.com/shadcn-ui/cn) 包，不是 `clsx` + `tailwind-merge` 的手写组合，也不是同名抢注包。

## TypeScript 是 6 / 7 双版本并存，不要"修正"成单版本

`package.json` 里这两行是**刻意**的，不是写错：

```json
"@typescript/native": "npm:typescript@^7.0.2",
"typescript": "npm:@typescript/typescript6@^6.0.2"
```

原因：typescript-eslint 至今不支持 TS 7（见 [typescript-eslint#10940](https://github.com/typescript-eslint/typescript-eslint/issues/10940)），把 `typescript` 直接装成 7.x 会让 `pnpm lint` 整个报错退出，而不是只报几条警告。所以按 TypeScript 官方的 side-by-side 方案，让 `typescript` 这个名字解析到 TS 6 的 API 包供 typescript-eslint 与 `next build` 使用，TS 7 则通过 `@typescript/native` 单独提供。

由此产生三个可执行文件，用途不同：

- `pnpm typecheck` → `tsc`，即 **TS 7**，原生 Go 编译器，日常类型检查用它（当前空项目下比 TS 6 快约 4 倍）。
- `pnpm exec tsc6` → **TS 6**，与 `next build` 内部所用版本一致。
- `pnpm lint` / `pnpm build` → 内部走 TS 6。

**因此 `pnpm typecheck` 通过不等于 `pnpm build` 通过**：两者是不同编译器。提交前两个都要跑。等 typescript-eslint 支持 TS 7 后，这套并存可以拆掉，届时删掉 `@typescript/native`、把 `typescript` 换回 `typescript@7` 即可。

## 提交前门禁

```bash
pnpm lint         # ESLint（内部 TS 6）
pnpm typecheck    # TS 7
pnpm build        # Next.js 构建，内部 TS 6
```

## 与后端的边界

Console 只调用控制面的 Admin API，不直连 Gateway 数据面、不直连 Agent。根仓库 [AGENTS.md](../../AGENTS.md) 的安全红线同样适用：API Key 明文、完整 Prompt、工作流 JSON 不得进日志或错误提示，前端展示 API Key 一律用后端返回的展示形式（`common/apikey` 定义），不要自己拼。
