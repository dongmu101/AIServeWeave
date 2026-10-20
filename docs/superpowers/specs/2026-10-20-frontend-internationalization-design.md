# 全部前端页面的八语言支持

日期：2026-10-20。状态：设计稿，供维护者审阅；尚未实现。

## 目标与已确认要求

维护者要求「所有前端页面要实现多国语言」，并确认首批语言为简体中文、繁体中文、英文、日文、韩文、德文、法文、西班牙文。

完成后的使用方式：访客首次打开页面时根据浏览器偏好选择语言；用户可以在登录前和登录后切换语言，选择在刷新、导航和再次访问时保留。页面正文、导航、操作提示、可访问名称及格式化结果保持一致。

以下是本设计采用的产品默认值，供本次审阅一并确认：无匹配语言时使用简体中文；保留当前 URL；语言仅作为浏览器显示偏好，不写入租户或平台会话。三个独立 Web 服务分别记住选择，不宣称不同域名间自动同步。

## 当前实现与覆盖范围

当前仓库没有统一的国际化模块或国际化依赖。Console 根布局固定 `lang="zh-CN"`，页面、共享组件、校验函数和错误映射直接包含中文；Agent 设置页使用 Go 内嵌 HTML 模板；Gateway 的 `/docs/` 提供仓库内置的 Swagger UI。Console 的 ComfyUI 编辑器是经 wujie 挂载的官方前端，其语言需要单独传递。

### Console

当前共有 38 个 `page.tsx` 入口，其中包含跳转入口。实现按下面的路由清单审计，跳转入口需要保留语言偏好，其余页面需要覆盖全部自有界面文案。

| 区域 | 路由 |
| --- | --- |
| 公共入口 | `/`、`/login`、`/signup`、`/operator`、`/operator/login` |
| 租户概览与发现 | `/console`、`/console/fleet`、`/console/models` |
| 租户管理 | `/console/users`、`/console/keys`、`/console/quota`、`/console/audit` |
| 租户任务 | `/console/workflows`、`/console/jobs`、`/console/jobs/history`、`/console/jobs/history/[id]` |
| 租户请求、用量与账户 | `/console/requests`、`/console/usage`、`/console/account`、`/console/settings` |
| 租户 API 参考 | `/console/docs`、`/console/docs/[slug]` |
| 平台机群与模型 | `/operator/fleet`、`/operator/models`、`/operator/model-pulls`、`/operator/comfyui-managed` |
| 平台发布 | `/operator/routes`、`/operator/workflows`、`/operator/workflow-templates` |
| 平台观测 | `/operator/metrics`、`/operator/alerts`、`/operator/alert-rules`、`/operator/requests`、`/operator/usage`、`/operator/audit` |
| 平台账户与文档 | `/operator/operators`、`/operator/account`、`/operator/api-docs` |

页面下的弹窗、表格、分页、筛选、Toast、空状态、加载与失败状态、图表图例和 Tooltip、placeholder、title、aria-label、页面标题及 metadata 都在范围内。对应文案散布于 `app/`、`components/console/`、`components/ui/` 和 `lib/console/`，不能只替换 JSX 正文。

### Agent 本地设置页

`service/aiServeWeaveAgent/configui/` 的全部菜单、配置分组、模型目录、Gateway 状态、保存确认与失败提示均在范围内。`/status/gateway` 返回的局部 HTML 与完整页面必须使用同一种语言，模型与状态刷新也不能恢复成中文。

### Gateway API 文档页

`service/aiServeWeaveGateway/docsui/` 的 `/docs/` 纳入范围，增加语言选择并翻译其自有界面及仓库拥有的文档说明。Swagger UI 控件需要通过独立的适配层接入，不能只翻译页面标题或直接改写压缩后的第三方 bundle。`/openapi.yaml` 的协议字段、operationId、参数名、枚举值和示例字节保持不变；说明文字的本地化作为展示资源处理。

### 内嵌 ComfyUI 编辑器

Console 外框和编辑器内部跟随同一个选择。已核对固定版本 `1.55.17` 的[官方语言清单](https://github.com/Comfy-Org/ComfyUI_frontend/blob/v1.55.17/src/locales/localeConfig.ts)：其中有 `zh`、`zh-TW`、`en`、`ja`、`ko`、`fr`、`es`，没有 `de`。其[语言解析实现](https://github.com/Comfy-Org/ComfyUI_frontend/blob/v1.55.17/src/i18n.ts)会将未提供的语言回退为英文，因此仅传入德文 locale 不能完成需求。

本设计选择固定上游源码版本的本地化构建：只增加德文的 main、nodeDefs、commands、settings 四组资源和语言登记，构建产物不进入 Console bundle，不把上游整站源码纳入仓库。维护源码版本与归档 SHA-256、独立且可审查的本地化补丁、锁定的依赖安装和产物清单；下载及构建由现有获取脚本的明确构建步骤完成，控制面仍只提供产物目录。原发布包的 SHA-256 不能被当作本地化产物的校验值，产物清单单独记录构建结果哈希。

这要求同步更新 Console AGENTS 和部署文档中「官方发布包原样提供」的现有约定，替换为「固定上游版本、仅受控语言资源补丁」。这是本设计需要维护者明确接受的一项约定变更，当前获取脚本和编辑器产物尚未修改。网络允许列表、会话边界、核心扩展范围和不提供自定义节点 JavaScript 的约束继续生效。

语言映射为 `zh-CN → zh`、`zh-TW → zh-TW`、`en-US → en`、`ja-JP → ja`、`ko-KR → ko`、`de-DE → de`、`fr-FR → fr`、`es-ES → es`。既有桥接扩展增加只接收语言字符串的接口，页面初次挂载与后续切换都调用它；不得为切换语言销毁编辑器或重载图。八种语言逐一验收，未支持的语言不能以英语回退冒充该语言已完成。

## 方案选择

| 方案 | 影响 | 结论 |
| --- | --- | --- |
| 显式翻译调用 + 类型化词典 + Cookie 偏好 | 保持当前路由和权限入口，服务端与客户端共享语言解析规则；翻译资源随构建发布 | 推荐 |
| 为每种语言增加 URL 前缀 | 需要迁移 App Router 目录、链接、登录回跳和代理匹配规则；对管理后台没有必要的独立索引收益 | 本次不采用 |
| 运行时翻译网页文本 | 无法可靠覆盖 React 重渲染、表单属性、错误提示及第三方画布；翻译质量与网络产生额外依赖 | 本次不采用 |

采用第一种方案。Intl、React Context、Next 请求 API 和 Go 标准库可承载核心能力，不为基本语言解析、词典查找和格式化增加第三方依赖。Swagger UI 使用其[官方插件接口](https://swagger.io/docs/open-source-tools/swagger-ui/customization/overview/)，复用 bundle 内的 React；ComfyUI 本地化构建使用该固定版本自身的工具链和锁文件，不向 Agent、Gateway 或控制面引入新的运行时依赖。

## 语言与资源契约

### 支持语言

| 语言 | 规范 locale | 选择器显示名称 |
| --- | --- | --- |
| 简体中文 | `zh-CN` | 简体中文 |
| 繁体中文 | `zh-TW` | 繁體中文 |
| 英文 | `en-US` | English |
| 日文 | `ja-JP` | 日本語 |
| 韩文 | `ko-KR` | 한국어 |
| 德文 | `de-DE` | Deutsch |
| 法文 | `fr-FR` | Français |
| 西班牙文 | `es-ES` | Español |

浏览器偏好按 `Accept-Language` 的权重和原有顺序匹配；`q=0` 不匹配。无效值被忽略。中文先匹配脚本：`zh-Hant` 为繁体、`zh-Hans` 为简体；没有脚本时，`zh-TW`、`zh-HK`、`zh-MO` 匹配繁体，`zh-CN`、`zh-SG` 及无地区的 `zh` 匹配简体。其余语言按语言子标签匹配，例如 `en-GB` 匹配 `en-US`。通配符不引入不受支持的 locale。

优先级为用户已经保存的有效选择、浏览器首选语言、简体中文默认值。非法或陈旧的 Cookie 不应导致异常、重定向循环或任意资源路径加载。

偏好 Cookie 分别命名为 `aisw_console_locale`、`aisw_agent_locale`、`aisw_docs_locale`，避免同一主机不同端口共享 Cookie 导致站点相互覆盖。值只允许八个规范 locale，设置 `Path=/`、`SameSite=Lax`、一年有效期，HTTPS 时设置 Secure；它是可由客户端读取的界面偏好，与 HttpOnly 鉴权 Cookie 独立。

### 词典

Console 在 `lib/i18n/` 下维护 locale 契约、翻译键及八份同形词典。按通用组件和业务域组织键，使用有含义的稳定标识；页面显式调用翻译接口，不使用 DOM 遍历、MutationObserver 或全局可变语言状态。

动态数量、名称和时长通过具名参数插入完整句子，不拼接词片。类型和测试约束每个 locale 拥有相同键、非空译文及相同参数集合；需要按数量变化的文案通过 `Intl.PluralRules` 选择句式。未知翻译键在开发和测试中失败；生产环境只作受控回退，不渲染 `undefined` 或内部键。

Agent 与 Gateway 使用各自可嵌入二进制的 JSON 资源，键结构在服务内保持一致，并与 Console 共用八个规范 locale、默认值和浏览器匹配测试样例。展示资源属于各自服务，避免将服务内部界面词典升级为隧道契约。

翻译均随制品分发，访问页面和切换语言不请求在线翻译服务。

## Console 渲染与切换

服务端请求作用域读取语言 Cookie 与 `Accept-Language`，初次渲染即使用对应的 `<html lang>`、metadata 及翻译资源。根布局把相同 locale 和所需词典交给客户端 Provider，保证首屏与 hydration 一致；服务端绝不通过模块级变量共享用户语言。

登录、注册、平台登录和两个已登录页面外壳都提供可用键盘操作的语言选择器。切换时更新 Provider 和偏好 Cookie，同步文档语言与标题，更新后续请求使用的语言。优先原地更新客户端界面，保留当前页、筛选条件、打开的弹窗和未提交表单；需要服务端更新的文案采用保持组件状态的刷新方式。选择器使用各语言的自称，长文案及八个选项在窄屏仍可操作。

格式化函数显式接收 locale 或在组件内使用 locale 绑定的包装器。移除固定的 `toLocaleString("zh-CN")` 和中文相对时间；日期、数字、百分比、字节与持续时间按当前语言展示，同时保持原有时区及业务数值含义。排序、筛选、请求参数和 CSV 的机器字段不依赖翻译后的标签。

错误层继续使用封闭的 `ApiErrorKind`。界面在显示时按当前 locale 映射固定安全文案，已经发生的错误在切换语言后也能更新；不翻译或展示上游任意错误文本。`limitProblem` 等校验函数需要返回稳定原因或在调用边界接收 locale，不能在状态中永久缓存中文句子。

## 文档与动态内容

所有仓库维护的 API 文档标题、分类、摘要、提示、参数说明、响应说明与补充章节均提供八语言资源，示例面板和导航使用同一 locale。

内置内容的翻译绑定对应的原始内容版本，维护者对文档作自定义修改后不能误用旧译文。API 文档编辑页的按钮和帮助文字翻译，运维输入的文档正文、业务名称、模型 ID、标签值、工作流描述与用户生成内容保持作者原文。它们是数据，不把自动翻译后的内容写回数据库。

代码示例、HTTP 方法/路径、JSON 键、协议枚举、配置项名、错误代码、凭据和具体请求内容不进入通用文本替换。表格中展示的状态名称可以翻译，实际发送的状态值保持原契约。

## Agent 与 Gateway 行为

Agent 由请求作用域解析 locale，完整页面、POST 保存结果和局部状态响应共用它。语言切换独立于保存操作，不修改 Agent YAML，也不丢失正在编辑的值。JavaScript 模式原地翻译，不提交配置表单；JavaScript 不可用时，通过明确的「仅切换语言」POST 动作回显当前表单值并保存语言偏好，不调用配置保存逻辑。完整页面和局部 HTML 响应提供相符的 Content-Language。

Gateway 文档选择器在文档初始化前确定 locale，按选择加载本地展示词典；语言切换不触发认证或发送 API 调试请求。Swagger 适配层不得修改 operation 参数、认证输入或复制内容；若需重建文档视图，应先保留当前展开项及已经输入的值。

三处站点保存自己的显示偏好，不把语言值加入登录 Cookie 密封载荷、后端鉴权参数、租户配额、审计事件或隧道消息。

## 验收与质量门禁

1. 默认 runner 覆盖八语言词典键/插值一致、locale 规范化、中文脚本与地区映射、`Accept-Language` 权重及非法 Cookie 回退、数值/日期/复数格式、错误原因本地化和请求间隔离。
2. 按前述路由表审计所有页面、共享组件及纯逻辑文案；以显式允许列表区分注释、代码示例、业务数据与应翻译的界面字面量，新增界面文案不能静默绕过词典。
3. Playwright 在隔离回环服务上覆盖八种语言切换、登录前后、刷新后偏好恢复、浏览器首次识别、租户与平台切换、旧错误/新错误、弹窗及 Toast。全部页面均有覆盖记录，不用单一登录页代表全站。
4. 在德文、法文及窄屏下检查导航、表格、弹窗和按钮，验证长译文不会遮挡操作；切换语言后未提交表单、筛选条件及编辑器图保留。
5. Agent 使用 httptest 覆盖全部语言的完整页面、保存响应、局部 Gateway 状态及回退；Gateway 文档测试覆盖 locale 资源、渲染与协议字段不变；内嵌编辑器逐一验证八种语言和宿主同步。
6. Console 跑通 `pnpm lint`、`pnpm typecheck`、`pnpm test`、`pnpm build` 和相关 `pnpm test:e2e`。测试使用现有 Node 内置 runner 与 Playwright，不增加新的测试框架。
7. Go 代码变更执行根 AGENTS 要求的 gofmt、vet、build、proto 生成一致性、全量测试及服务 race 测试，默认测试不依赖真实服务、GPU 或外部网络。
8. 同步 Console、Agent、Gateway 的 README 及现有状态文档，说明支持语言、默认值、偏好范围和添加语言的方法。完成后分别报告代码覆盖与实际浏览器验收结果。

## 实施边界与拆分

先实现并验证 locale 解析、词典及服务端/客户端渲染契约；再迁移公共入口和共享组件、租户页面、平台页面与内置文档；随后完成 Agent/Gateway 的展示资源和 ComfyUI 桥接；最后按路由表逐项验收并补齐文档。

所有子步骤属于本次八语言目标，基础设施或部分页面完成不能被报告为全站完成。语言工作不改变现有权限、服务端转发白名单、会话隔离、凭据边界、隧道协议或第三方前端的网络限制。
