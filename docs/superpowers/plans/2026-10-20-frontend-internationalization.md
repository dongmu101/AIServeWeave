# 前端八语言支持 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Console 全部页面、Agent 设置页、Gateway 文档页和内嵌 ComfyUI 均支持已确认的八种语言，切换后保留用户正在编辑的内容。

**Architecture:** Console 采用请求作用域的服务端语言解析、React Provider 和显式词典调用；两个 Go 页面使用本地嵌入资源。语言解析规则通过跨语言测试样例保持一致；ComfyUI 固定上游源码版本补充德文，通过既有字符串桥接同步语言。

**Tech Stack:** Next.js 16.3.4、React 19、TypeScript 6/7 并存、Node 24 内置测试、现有 Playwright、Go 标准库、Swagger UI 插件、ComfyUI_frontend 1.55.17。

**Spec:** [八语言设计](../specs/2026-10-20-frontend-internationalization-design.md)。本文件是实施计划，下面的测试及实现步骤尚未执行。

## Global Constraints

- locale 固定为 `zh-CN`、`zh-TW`、`en-US`、`ja-JP`、`ko-KR`、`de-DE`、`fr-FR`、`es-ES`；无匹配时使用 `zh-CN`。
- 保留当前 URL；优先级为有效偏好 Cookie、浏览器语言、默认值；中文脚本优先于地区；`q=0` 不参与匹配。
- Cookie 分别为 `aisw_console_locale`、`aisw_agent_locale`、`aisw_docs_locale`，`Path=/`、`SameSite=Lax`、`Max-Age=31536000`，HTTPS 时 Secure。
- 翻译资源随制品分发，不请求在线翻译服务；基本国际化功能不新增第三方依赖。Agent、Gateway 的既有依赖边界保持不变。
- 不用模块级可变语言、不用 DOM 文本匹配来翻译 Console，不以 `key={locale}` 重挂载页面或编辑器。
- 数字和日期只改变显示格式，保留浏览者本地时间的既有语义；协议字段、代码示例、凭据、业务名称和用户输入保持原值。
- 所有自有标题、导航、表格、图表、弹窗、Toast、错误、校验提示、placeholder、title、aria-label 和 metadata 都属于翻译范围。
- 新增/改动的代码注释中英双语；Go 导出注释以标识符开头；Node 直接测试的相对 import 带扩展名。
- 不修改隧道协议、不扩大转发范围、不改变权限、租户/平台会话或错误脱敏规则。
- ComfyUI 保持源码版本 `1.55.17`、节点目录版本 `0.41.2` 和 678 个核心节点；本地化产物记录独立校验值。
- 每项先完成所列行为的失败测试，再实现、复测并仅提交该项文件。执行前遵循 using-git-worktrees，使用隔离工作区；不推送或部署。

## Review Focus

1. 快速连续切换语言且资源乱序返回：最后一次选择生效，加载失败时保留旧界面和旧 Cookie；归任务 3。
2. 两名用户或两个页签使用不同语言：服务端不串语言，登录/退出不清除显示偏好，同主机不同端口不互相覆盖 Cookie；归任务 1、3、12。
3. 页面已报错、弹窗或 Toast 仍打开：切换后文案更新，操作值、筛选条件和未提交草稿保留；归任务 4、5、6。
4. 内置 API 文档被运维修改：自定义正文不套用旧翻译，代码示例和占位参数不被替换；归任务 7、9。
5. 编辑器已编辑但未发布：切换不重载图、不增加脏标记；同一图的导出及摘要不随显示语言改变，自定义标题保留；归任务 11。

## 文件职责与执行顺序

下文使用明确的路径前缀缩写：`C/ = service/aiServeWeaveConsole/`，`A/ = service/aiServeWeaveAgent/configui/`，`G/ = service/aiServeWeaveGateway/`，`E/ = service/aiServeWeaveControlPlane/internal/comfyeditor/`；其余路径相对仓库根目录。

| 单元 | 新增/修改位置 | 职责 |
| --- | --- | --- |
| 语言契约 | `common/browserlang/`、`C/lib/i18n/locale.ts` | 八语言、浏览器匹配、Cookie 值校验；共用测试样例 |
| 词典与格式化 | `C/lib/i18n/{messages,translate,format}.ts`、`C/lib/i18n/messages/` | 类型、加载器、插值、复数及显示格式 |
| 服务端与交互入口 | `C/lib/server/locale.ts`、`C/components/i18n/`、`C/app/layout.tsx` | 首屏一致、选择器、偏好持久化 |
| 控制台页面迁移 | `C/app/`、`C/components/console/`、`C/components/ui/`、相关 `C/lib/console/` | 将自有文案及纯逻辑输出接入翻译 |
| 内置 API 文档 | `C/lib/i18n/docs/`、`C/scripts/sync-i18n-docs.mjs` | 原始内容快照、版本匹配及只读翻译投影 |
| Go 页面 | `A/i18n.go`、`A/i18n/`、`G/docsui/`、`G/docs.go` | 完整页、局部页、文档控件及说明 |
| 编辑器资源 | `scripts/comfyui-localization/`、`scripts/fetch-comfyui-frontend.sh` | 固定源码构建、德文资源与制品清单 |
| 编辑器桥接 | `E/bridge.js`、`C/components/console/comfy-editor.tsx` | 初次挂载及后续切换、导出稳定性 |
| 验收清单 | `docs/acceptance/i18n-2026-10-20/`、`C/e2e/i18n*.spec.ts` | 38 个入口 × 8 种语言和服务专项证据 |

控制台轨道为任务 1–7；Agent 为任务 1、8；Gateway 为任务 1、9；编辑器为任务 2–3、10–11。各轨道可独立验收，最终由任务 12 合并确认全站覆盖；任何单轨完成都不等于整个目标完成。

### Task 1：语言解析契约与跨语言一致性

**Files:** 新增 `common/browserlang/{locale.go,locale_test.go,testdata/negotiation.json}`、`C/lib/i18n/{locale.ts,locale.test.ts}`。
**Interfaces:** TS 导出 `Locale`、`LOCALES`、`DEFAULT_LOCALE`、`parseLocale(value: string): Locale | null`、`resolveLocale(saved: string | null, acceptLanguage: string): Locale`；Go 导出 `type Locale string`、`Parse(string) (Locale, bool)`、`Resolve(saved, acceptLanguage string) Locale`、`PreferenceCookie(name string, locale Locale, secure bool) *http.Cookie`。Cookie 只接受八个规范值，浏览器标签可匹配语言/脚本/地区。

- [ ] **Step 1:** 写表驱动测试，共用 JSON 样例覆盖全部 locale、繁简脚本优先、权重/同权重顺序、非法 q、q=0、非法 Cookie、空值和默认值。例如 TS 与 Go 都须得到：
  ```ts
  assert.equal(resolveLocale(null, "en;q=0,zh-Hant-HK;q=0.8,de;q=0.7"), "zh-TW");
  assert.equal(resolveLocale("de-DE", "fr-FR"), "de-DE");
  assert.equal(resolveLocale("../../xx", "ja-JP"), "ja-JP");
  ```
- [ ] **Step 2:** 根目录运行 `go test ./common/browserlang`，在 C 运行 `node --test lib/i18n/locale.test.ts`；预期新模块尚未实现而失败。
- [ ] **Step 3:** 实现这些接口；只读白名单解析，不从 locale 拼接任意文件路径。Cookie 属性按 Global Constraints；测试同主机三种 Cookie 名称可以同时保留。
- [ ] **Step 4:** 重跑两组测试，预期全部通过且读取同一份 negotiation.json；测试断言 Go/TS 每条样例结果一致。
- [ ] **Step 5:** 提交本任务文件，消息 `feat(i18n): add shared locale negotiation contract`。

### Task 2：词典、插值、复数和格式化基础

**Files:** 新增 `C/lib/i18n/{messages.ts,translate.ts,format.ts,catalog.test.ts,translate.test.ts,format.test.ts}`；新增 `C/lib/i18n/messages/<八个 locale>/{index,common,auth,tenant,operator,docs}.ts`。
**Interfaces:** `MessageKey` 为简体中文源词典的键集合；`Messages` 为同键词典；`MessageValues = Readonly<Record<string, string | number>>`；`Translator = (key: MessageKey, values?: MessageValues) => string`；`loadMessages(locale: Locale): Promise<Messages>`、`createTranslator(locale: Locale, messages: Messages): Translator`。格式化接口均返回 string：`formatNumber(value: number, locale: Locale, options?: Intl.NumberFormatOptions)`、`formatPercent(ratio: number, locale: Locale)`（1 表示 100%）、`formatBytes(bytes: number, locale: Locale)`（1024 进制 IEC 单位）、`formatDuration(milliseconds: number, locale: Locale)`、`formatRelativeTime(value: number, unit: Intl.RelativeTimeFormatUnit, locale: Locale)`；调用方按原业务单位换算。

- [ ] **Step 1:** 写词典键/非空/插值一致性测试和语义断言：
  ```ts
  assert.equal(createTranslator("de-DE", await loadMessages("de-DE"))("common.save"), "Speichern");
  assert.equal(createTranslator("en-US", await loadMessages("en-US"))("common.items", { count: 2 }), "2 items");
  ```
  同时测试 `fr-FR` 的 0/1/2 数量、含尖括号的用户名作为普通字符串、缺少参数和未知键在开发/测试中报错；生产未知键受控回退。
- [ ] **Step 2:** 在 C 运行 `node --test lib/i18n/*.test.ts`，预期上述新接口缺失而失败。
- [ ] **Step 3:** 使用固定动态 import 映射按语言加载资源；每个消息为字符串或 `{ one?: string; other: string }`，通过 `Intl.PluralRules` 选完整句式；用 Intl 实现上述格式化接口。先填基础词条，后续任务新增业务词条时同时补齐八种语言。
- [ ] **Step 4:** 重跑测试，再依次执行 `pnpm build`、`pnpm typecheck`，确保隔离工作区先生成 `.next/types`；预期键、参数和类型检查通过，0 配额与缺失时间的业务含义不由格式化函数改变。
- [ ] **Step 5:** 提交，消息 `feat(i18n): add typed catalogs and formatters`。

### Task 3：服务端首屏、Provider 和所有入口的语言选择器

**Files:** 新增 `C/lib/server/locale.ts`、`C/components/i18n/{provider,language-switcher,message}.tsx`、`C/lib/i18n/selection.ts`、`C/lib/i18n/selection.test.ts`、`C/e2e/i18n-entry.spec.ts`；修改 `C/app/layout.tsx`、`C/app/{login,signup}/`、`C/app/operator/login/`、`C/app/console/console-shell.tsx`、`C/app/operator/operator-shell.tsx`、`C/playwright.config.ts`。
**Interfaces:** `readLocale(): Promise<Locale>` 只在服务端请求作用域读取；`I18nProvider({ initialLocale, initialMessages, children })` 的属性类型为 `{ initialLocale: Locale; initialMessages: Messages; children: React.ReactNode }`；`useI18n(): { locale: Locale; t: Translator; setLocale(locale: Locale): Promise<void>; changing: boolean }`；`Message({ id: MessageKey, values?: MessageValues })` 用于需要随语言更新的 React 文案。`selection.ts` 提供 `createLocaleSelection(initial: Locale, load: typeof loadMessages, commit: (next: { locale: Locale; messages: Messages }) => void): { select(locale: Locale): Promise<void> }`，供乱序/失败单测。

- [ ] **Step 1:** 写测试：de 慢/fr 快最终只提交 fr；加载失败不写 Cookie；并发请求 en/ja 的响应 html lang 各自正确；首次浏览器 zh-Hant 匹配繁体。入口浏览器断言示例：`await expect(page.locator("html")).toHaveAttribute("lang", "ja-JP")`，并独立断言日文登录按钮文案。
- [ ] **Step 2:** 在 C 运行 `node --test lib/i18n/selection.test.ts`；构建后运行 `pnpm test:e2e -- e2e/i18n-entry.spec.ts`，预期新入口行为失败。
- [ ] **Step 3:** 根布局使用 async headers/cookies 和相同的初始词典设置 html lang、metadata、Provider；按项目自带 Next 文档实现。选择成功后才写 Cookie；调用保持组件状态的 router.refresh，避免旧服务端结果覆盖新选择；同步标题。给公共入口及两个 shell 加选择器，补齐 auth 词典。现有浏览器测试默认 locale 明确设为 zh-CN，新用例显式覆盖语言。
- [ ] **Step 4:** 重跑本项测试；验证刷新、登录/退出、租户/平台跳转保留语言，输入中的邮箱/密码不因切换丢失；禁止把 locale 加入会话载荷或鉴权。
- [ ] **Step 5:** 提交，消息 `feat(console): add persistent language selection`。

### Task 4：共享组件、错误及校验显示

**Files:** 修改 `C/components/console/{states,pager,submit-button,confirm-dialog,data-table,fleet-freshness,account-security}.tsx`、`C/components/ui/{dialog,select,sonner}.tsx` 的自有可访问文案；修改 `C/components/console/{use-resource,use-paged-resource}.ts`、`C/lib/console/{errors,format,signup,node-ops,model-routes,workflow-templates,comfy-graph,model-pull,comfyui-managed}.ts` 及对应测试；新增 `C/lib/console/{errors,signup}.test.ts`、`C/e2e/i18n-shared.spec.ts`。
**Interfaces:** `describe(error: unknown, locale?: Locale): string` 的默认值为 zh-CN，仍仅按 ApiErrorKind 渲染安全文案；`Resource.error` 对外仍为字符串，但内部保存错误种类而非已翻译句子。格式化/验证函数在现有参数后增加可选 `locale: Locale = DEFAULT_LOCALE`；状态中的验证错误保存原因及参数，在渲染时翻译。

- [ ] **Step 1:** 扩展单测覆盖 `describe(new ApiError("forbidden"), "en-US")` 的固定英文文案、任意上游错误不外泄、空配额/负数/溢出、过期 Key、未知状态、无记录与永不过期。浏览器先触发错误并打开确认弹窗，再切换德文，断言新文案和原输入值。
- [ ] **Step 2:** 运行对应 `node --test lib/console/{errors,format,signup,node-ops,model-routes,workflow-templates,comfy-graph,model-pull,comfyui-managed}.test.ts lib/i18n/*.test.ts`；新增本地化断言应失败，既有业务测试应保留。
- [ ] **Step 3:** 迁移标签常量、TTL、审计动作、配额说明、纯逻辑校验和共享状态；请求 key 不包含 locale，语言变化不重复业务读取。可见 Toast 使用 `<Message>`，未知状态/用户诊断数据按既有规则显示，不将任意英文异常送屏。
- [ ] **Step 4:** 运行上述测试及 `pnpm test:e2e -- e2e/i18n-shared.spec.ts`；验证失败请求仍为失败状态、取消请求不显示错误、请求和写操作次数不因切换增加。
- [ ] **Step 5:** 提交，消息 `feat(console): localize shared states and validation`。

### Task 5：租户页面全部迁移

**Files:** 修改 `C/app/console/page.tsx` 及 `account/`、`audit/`、`fleet/`、`jobs/`（含 history 和详情）、`keys/`、`models/`、`quota/`、`requests/`、`settings/`、`usage/`、`users/`、`workflows/` 下的 page/view/dialog/actions；补齐八份 tenant 词典；新增 `C/e2e/i18n-tenant.spec.ts`，扩展 `C/e2e/fixtures.ts`。
**Interfaces:** 消费任务 2–4 的 `useI18n`、`Message`、格式化及错误接口；筛选值、游标、角色值、TTL 秒数、导出数据及请求契约保持既有类型。

- [ ] **Step 1:** 按设计的 15 个租户非文档入口建立八语言用例；涵盖创建用户/Key、用户操作、配额错误、Gateway Key 设置及 Job 取消提示；断言 `common.save` 八种独立预期文案和路由对应的独立标题样例，不能只检查是否存在翻译键。
- [ ] **Step 2:** 在 C 构建并运行 `pnpm test:e2e -- e2e/i18n-tenant.spec.ts`，预期尚未迁移的标题及提示失败。
- [ ] **Step 3:** 逐页迁移完整句子和可访问属性，表格列和 useMemo 回调依赖当前翻译器；日期保持浏览者本地时区。对共享视图只改一处，新增业务词条同时翻译八种语言。
- [ ] **Step 4:** 重跑本项浏览器测试、`pnpm test`、`pnpm typecheck`；切换中保留筛选条件、游标历史、未提交配额和未复制的 Key；机器字段和值与切换前严格相同。
- [ ] **Step 5:** 提交，消息 `feat(console): localize all tenant pages`。

### Task 6：平台页面全部迁移

**Files:** 修改 `C/app/operator/(protected)/` 的全部 page/view（文档正文投影留给任务 7）、`C/app/operator/alert-rules/alert-rules-view.tsx`、`C/app/operator/alerts/alerts-view.tsx`、`C/lib/console/{metrics,metrics-charts,usage,fleet}.ts` 中的展示文案；补齐八份 operator 词典；新增 `C/e2e/i18n-operator.spec.ts`。
**Interfaces:** 消费任务 2–4；模型路由、工作流发布、CAS 修订号、维护/禁用动作和图表数据的接口保持既有契约，图例、Tooltip 和确认文字在渲染时翻译。

- [ ] **Step 1:** 八语言覆盖设计中的 16 个受保护平台入口，增加路由草稿、模板草稿、API 文档编辑框、模型拉取确认和告警规则表单保留用例。例如填写路由 model 后切换法文，断言 model 原值及 expected_revision 不变。
- [ ] **Step 2:** 在 C 构建后运行 `pnpm test:e2e -- e2e/i18n-operator.spec.ts`，预期现有中文平台文案失败。
- [ ] **Step 3:** 迁移所有自有文案、筛选选项、图例/Tooltip 和错误提示；纯验证输出按任务 4 的原因/参数模式接入；状态值和操作体使用原枚举。
- [ ] **Step 4:** 重跑本项测试、`pnpm test`、`pnpm typecheck`；以德文/法文检查 375px 导航、长标题、发布确认和图表，不因切换触发发布、回滚或服务操作。
- [ ] **Step 5:** 提交，消息 `feat(console): localize operator pages and charts`。

### Task 7：内置 API 文档的版本匹配翻译

**Files:** 新增 `C/lib/i18n/docs/{source.ts,localize.ts,localize.test.ts}`、`C/scripts/sync-i18n-docs.mjs`；补齐八份 docs 词典；修改 `C/app/console/docs/`、`C/components/console/api-doc-article.tsx`、平台 API 文档页预览；新增 `C/e2e/i18n-docs.spec.ts`。
**Interfaces:** `localizeApiDocSummary(doc: ApiDocSummary, locale: Locale, messages: Messages): ApiDocSummary`；`localizeApiDoc(doc: ApiDocDetail, locale: Locale, messages: Messages): ApiDocDetail`。只返回展示投影，不修改原对象；`source.ts` 从控制面 `internal/apidocs/seed/*.json` 的 19 份内置原文生成，记录稳定内容指纹与按字段的翻译键。

- [ ] **Step 1:** 写原文版本命中、内容修改、未知 slug、字段顺序变化和嵌套参数测试：
  ```ts
  assert.deepEqual(localizeApiDoc(customDoc, "de-DE", de), customDoc);
  assert.equal(localizeApiDoc(seedDoc, "ja-JP", ja).content.request_examples[0].code, seedDoc.content.request_examples[0].code);
  ```
  快照检查所有自有说明均有八语言资源，示例占位符和参数枚举保持逐字一致。
- [ ] **Step 2:** 在 C 运行 `node --test lib/i18n/docs/localize.test.ts`，预期投影函数和快照缺失而失败。
- [ ] **Step 3:** 用固定结构的排序序列化生成内容指纹，排除 updated_at 等非原文字段；详情完整原文命中才投影，列表仅对命中的元数据字段投影。原文修改时整份详情保留作者版本。翻译标题、摘要、分类、说明、notice、章节及参数描述，不翻译示例、协议值和编辑框原始内容。
- [ ] **Step 4:** 在 C 运行 `node scripts/sync-i18n-docs.mjs --check`、本项单测和 `pnpm test:e2e -- e2e/i18n-docs.spec.ts`；断言 19 个内置 slug 都有资源且源文件不被改写，复制示例内容完全相同。
- [ ] **Step 5:** 提交，消息 `feat(console): localize versioned API documentation`。

### Task 8：Agent 设置页、保存结果及动态片段

**Files:** 新增 `A/i18n.go`、`A/i18n_test.go`、`A/i18n/<八个 locale>.json`、`A/assets/i18n.js`；修改 `A/{configui,gateway,models}.go` 及三个现有测试文件；按需将内嵌表单移至 `A/templates/form.html` 并 go:embed，保持原有入口。
**Interfaces:** 消费 `browserlang.Resolve`；包内 `newPageText(locale browserlang.Locale) pageText`、`pageText.Text(key string) string`、`pageText.Format(key string, values map[string]any) string`；`formValues` 增加请求作用域 Locale/文本资源；`renderForm` 和 Gateway 局部渲染接收同一 locale。JS 只通过显式 data-i18n 键更新自有文字/属性。

- [ ] **Step 1:** 用 httptest 表驱动覆盖八语言 GET/HEAD、保存成功/失败、Gateway 状态和模型目录；断言 Content-Language、Cookie 属性、表单回显及两次并发请求语言隔离。POST `action=locale` 后断言配置文件字节完全未变。
- [ ] **Step 2:** 根目录运行 `go test ./service/aiServeWeaveAgent/configui`，预期新增语言断言失败。
- [ ] **Step 3:** 嵌入静态词典；模板按键渲染，JS 可原地切换且不写配置。无 JS 使用独立 locale POST 分支，仅回显全部表单值；默认 save 分支保持兼容。连接/模型状态保留稳定原因并在渲染时翻译；保存失败显示固定安全文案，不拼接原始 err.Error()。
- [ ] **Step 4:** 重跑本包测试及 race；验证片段刷新保留当前 locale、非法语言回退、无 JS 切换不落盘，资源 JSON 不含表单值或凭据；浏览器步骤在任务 12 验证。
- [ ] **Step 5:** 提交，消息 `feat(agent): localize setup page and live status`。

### Task 9：Gateway 文档界面和 Swagger 适配

**Files:** 修改 `G/docs.go`、`G/docsui/index.html`；新增 `G/docs_test.go`、`G/docsui/{i18n,swagger-i18n}.mjs`、`G/docsui/i18n/<八个 locale>.json`、`G/docsui/i18n.test.mjs`。不改压缩的第三方 bundle 和 `G/openapi.yaml`。
**Interfaces:** 消费 `browserlang.Resolve`；JS 的 `Catalog` 为键到字符串的只读映射，`values` 为字符串/数值参数映射；导出 `translate(key, values, catalog): string`、`localizeSpecText(spec, catalog): object`、`createLocalizationPlugin(getLocaleState): object`，其中 `getLocaleState()` 返回 `{ locale, catalog }`。后者使用 Swagger 的插件接口及其 system.React；只改允许列表内的说明和控件文字，原组件身份与状态保持。

- [ ] **Step 1:** httptest 检查 `/docs` 跳转、八语言 `/docs/` 初始 lang/Content-Language 和资源可用；JS 测试断言说明已翻译，但 method/path、schema/enum/default/example、servers.url 与原对象相同。浏览器需覆盖 Authorize、Try it out、Execute、响应和模型详情。
- [ ] **Step 2:** 根目录运行 `go test ./service/aiServeWeaveGateway -run TestDocs`、`node --test service/aiServeWeaveGateway/docsui/i18n.test.mjs`，预期新增语言行为失败。
- [ ] **Step 3:** 精确处理 `/docs/` 与 `/docs/index.html` 的本地化初始页，其余资源仍由 embed FS 提供。选择器更新独立偏好和插件状态；对控件及 OAS 展示路径显式翻译，跳过代码/示例子树；未知原文保留原值并由资源覆盖审计识别缺口。
- [ ] **Step 4:** 重跑测试；比对 `/openapi.yaml` 字节；在任务 12 检查授权输入、展开项和参数输入在切换后保留，以及切换过程中没有发起业务 API 调试请求。
- [ ] **Step 5:** 提交，消息 `feat(gateway): localize API documentation UI`。

### Task 10：ComfyUI 八语言受控构建

**Files:** 新增 `scripts/comfyui-localization/{manifest.json,apply.mjs,apply.test.mjs,locale-config.patch,de/main.json,de/nodeDefs.json,de/commands.json,de/settings.json}`；修改 `scripts/fetch-comfyui-frontend.sh`、`E/comfyeditor.go`；新增 `E/comfyeditor_test.go`；更新 `C/AGENTS.md` 和部署说明中的制品规则。
**Interfaces:** `apply.mjs` 导出 `applyLocalization(sourceDir: string, resourcesDir: string): Promise<void>`，只接受 manifest 固定版本及预期原始文件摘要；构建输出 `aisw-i18n-manifest.json`，记录 source commit/归档哈希、补丁/资源哈希、工具链版本和逐文件产物哈希。脚本新增 `--localized DEST`，供八语言部署使用。

- [ ] **Step 1:** 在临时目录测试源版本/摘要不符、缺少 de 资源或插值不一致时构建前失败；验证固定 tag 对应 commit 和源码归档摘要写入 manifest 后才允许应用补丁。Go 测试拒绝缺少八语言清单或版本不符的已配置 DistDir。
- [ ] **Step 2:** 根目录运行 `node --test scripts/comfyui-localization/apply.test.mjs`、`go test ./service/aiServeWeaveControlPlane/internal/comfyeditor`，预期构建检查接口及清单校验缺失而失败。
- [ ] **Step 3:** 保持上游版本与节点目录不变，增加 de 的四组词典和登记；核对其余七种官方词典的实际覆盖，缺项仅以受控语言资源补丁补齐。临时目录下载源码、核验、使用其锁文件及固定工具链构建，移除 source map，生成独立产物清单；保留旧的原包获取方式用于兼容，但八语言部署必须使用 --localized 产物。
- [ ] **Step 4:** 重跑离线测试，再执行一次真实 `scripts/fetch-comfyui-frontend.sh --localized <临时产物目录>`；检查八语言资源、manifest 全部哈希和无 source map。该步骤需要依赖下载，不纳入默认离线测试。更新规则为固定源码与受控资源补丁，说明已配置编辑器的部署须先替换资源再升级服务。
- [ ] **Step 5:** 提交源码、补丁与文档，消息 `build(comfy): add reproducible localized frontend assets`；不提交下载源码、node_modules 或 dist。

### Task 11：编辑器实时语言桥接和导出不变量

**Files:** 修改 `E/bridge.js`、`E/comfyeditor.go`、`C/components/console/comfy-editor.tsx`、`C/lib/console/comfy-editor.ts` 及其测试、C 的 package.json/Playwright 配置和 `C/e2e/fixtures.ts`；新增 `C/lib/i18n/editor-locale.ts`、`C/lib/i18n/editor-locale.test.ts`、`C/e2e/i18n-editor.spec.ts`、`C/playwright.i18n.config.ts`、`E/browser_test.go`（browser build tag）。
**Interfaces:** `editorLocale(locale: Locale): string` 返回设计中的 zh/zh-TW/en/ja/ko/de/fr/es 映射；桥接增加 `setLocale(locale: string): Promise<void>`，只传规范 locale 字符串，拒绝未知值。其余 `loadWorkflow/loadApi/exportGraph/toggleAppMode` 接口和大小限制保持。

现有 `E/comfyeditor.go` 为导出稳定性固定 zh；上游 [graphToPrompt](https://github.com/Comfy-Org/ComfyUI_frontend/blob/v1.55.17/src/utils/executionUtil.ts) 将 node.title 写入 `_meta.title`。因此本任务既要接入显示语言，也要验证持久化标题和导出不受它影响。

- [ ] **Step 1:** 创建 browser-tag 编辑器回环测试服务及独立 Playwright 配置，读取任务 10 制品；复用合成会话与业务 mock，但 comfy-editor 前缀实际转发到该服务。默认配置忽略 editor/services 专项，新增 `test:e2e:i18n` 脚本。写映射及实际编辑器测试：载入同一图后切换八种语言，图节点/连线/输入/自定义标题不变，`exportGraph()` 摘要相同、onEdit 次数不增加；增加“挂载前切换”和“导出与切换重叠”用例。
- [ ] **Step 2:** 在 C 运行 `node --test lib/i18n/editor-locale.test.ts lib/console/comfy-editor.test.ts`，构建后运行 `pnpm test:e2e:i18n -- e2e/i18n-editor.spec.ts`，预期夹具能启动、语言桥接行为失败；不能把缺制品或服务启动失败当作功能的失败测试。
- [ ] **Step 3:** 宿主在 ready 后应用最新 locale，后续通过 effect 更新现有 bridge，不把 locale 放入挂载 effect 依赖；桥接串行处理语言操作并等待语言资源实际加载，失败向宿主返回安全错误。保留 zh 的规范初始化及图的持久化标题，切换仅改变显示，不重新生成节点 title/_meta；新节点默认持久化标题取核心目录原值，自定义标题原样保留。
- [ ] **Step 4:** 验证同一图在八种语言下导出一致、布局视口剥离仍有效、编辑器不重挂载、不重载图、不持久化会话工作流；语言资源及设置请求仍全部经过既有受限前缀。不能以等待固定时长代替 ready/加载完成事件。
- [ ] **Step 5:** 提交，消息 `feat(comfy): synchronize editor locale safely`。

### Task 12：完整覆盖审计、服务浏览器验收和最终门禁

**Files:** 新增 `C/scripts/audit-i18n.mjs`、`C/lib/i18n/{source-audit.ts,source-audit.test.ts}`、`C/e2e/i18n-coverage.spec.ts`、`C/e2e/i18n-services.spec.ts`；新增 `A/browser_test.go`、`G/docs_browser_test.go`（均 browser build tag），扩展任务 11 的编辑器夹具和独立 Playwright 配置；新增 `docs/acceptance/i18n-2026-10-20/{coverage.json,README.md}`；修改 C 的 package.json/默认 Playwright 配置、根 `README.md`/`STATUS.md`、四个受影响服务的 `README.md`、Console `STATUS.md` 和 `deploy/README.md`。
**Interfaces:** `auditSource(sourceText: string, fileName: string): readonly { file: string; line: number; column: number; text: string }[]` 使用既有 TS 6 parser 扫描 JSX 文本/属性和展示常量，排除注释；仅对代码示例、协议常量和业务数据作逐条理由及源内容指纹的允许列表。coverage.json 列出设计中的 38 个入口、最终路径、标题键和所需交互；browser-tag 测试服务仅监听回环并提供合成数据和任务 10 制品。

- [ ] **Step 1:** 审计器测试放入硬编码 UI 中文、模板字面量、title/aria-label 和 Toast，须报文件/位置；相同文字在注释/协议示例中允许。覆盖矩阵断言 `38 * 8 = 304` 个路由语言记录，重定向入口检查最终语言；未处理的 API 请求、pageerror、hydration 警告均使浏览器测试失败。
- [ ] **Step 2:** 运行新增源审计及覆盖测试，预期清单/规则尚未实现而失败；不能通过整文件忽略或允许当前所有原文来使审计变绿。
- [ ] **Step 3:** 补齐页面和词典缺口；扩展现有合成会话/API fixtures，不连接真实控制面。独立 i18n Playwright 配置补充 Agent/Gateway 的 browser-tag 回环服务；默认测试不启动这些服务。验证三个站点的 Cookie 不互相覆盖、无 JS Agent 切换、Swagger 输入保留及真实编辑器切换；源码审计的 TS 模块供单测和 CLI 共用。
- [ ] **Step 4:** C 执行 `pnpm lint`、`pnpm test`、`pnpm build`、`pnpm typecheck`、`pnpm test:e2e`、`pnpm test:e2e:i18n`；执行源审计和文档源快照 --check；根目录执行 `gofmt -l ./common ./service ./api`（无输出）、`go vet ./...`、`go build ./...`、`go generate ./api/...`（生成文件无 diff）、`go test ./...`、`go test -race ./service/...`。任务 10 真实构建已验证的同一份产物用于服务浏览器验收。
- [ ] **Step 5:** 把命令结果、304 条覆盖状态、八语言长文案截图及边界说明写入验收记录；未执行的检查明确标为未执行，不能记为通过。同步添加语言、更新词条、更新内置 API 文档和构建编辑器的维护指南；提交消息 `test(i18n): verify complete frontend language coverage`。

## 自审与交付

- 设计中的 38 个入口由任务 3、5、6、7、12 覆盖；共享提示/纯函数由任务 4 覆盖；内置文档、自定义内容及代码不变由任务 7、9 覆盖。
- Agent 动态片段及无 JS 由任务 8、12 覆盖；Gateway 控件状态由任务 9、12 覆盖；ComfyUI 德文资源、桥接和导出稳定性由任务 10、11、12 覆盖。
- Review Focus 五类情形均在其归属任务列出测试；交付不依赖在线翻译、真实 GPU 或真实业务账户。
- 推荐本会话逐项执行：核心接口贯穿多个轨道，由一个实现者连续维护可减少接口漂移；全部完成后安排独立代码审查。若选择子代理执行，按任务边界分派，不让多个实现者同时改 Provider、共享组件或词典聚合文件。
- 计划审阅并选择执行方式后才开始功能代码；最终交付包含改动摘要、运行过的门禁、浏览器验收证据和明确的部署资源要求。
