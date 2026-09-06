import type { ApiKeySummary } from "./contract.ts";

/**
 * Display rules that must not be reinvented per view: how a missing timestamp
 * reads, what a key's state actually is, how long a key may live, and what a
 * local filter is allowed to claim.
 *
 * 不该被各视图各自重新发明的展示规则：缺失的时间戳怎么读、一个 key 的真实状态是什么、
 * 一个 key 能活多久，以及本地筛选可以宣称什么。
 */

/**
 * UNKNOWN_TIME is what an absent timestamp reads as. The control plane omits
 * `last_used_at` and `expires_at` when they have no value, and an absent value
 * is a fact — "this key has never been used" — not a formatting accident. It
 * is never rendered as an epoch or a dash that could be mistaken for a date.
 *
 * UNKNOWN_TIME 是缺失时间戳的读法。控制面在 `last_used_at` 与 `expires_at` 没有值时
 * 省略它们，而值的缺席本身是一个事实——「这个 key 从未被使用过」——不是格式化的意外。
 * 它绝不会被渲染成纪元时间，或一个可能被误认为日期的短横线。
 */
export const UNKNOWN_TIME = "无记录";

/**
 * NEVER_EXPIRES is how a key with no expiry reads. The control plane stores no
 * expiry for a key created with a negative TTL, which is the explicit "never"
 * — distinct from a value it simply does not know.
 *
 * NEVER_EXPIRES 是没有过期时间的 key 的读法。控制面对以负 TTL 创建的 key 不存过期时间，
 * 那是显式的「永不」——与一个它单纯不知道的值不同。
 */
export const NEVER_EXPIRES = "永不过期";

/**
 * formatDateTime renders an RFC 3339 timestamp in the viewer's local time.
 *
 * formatDateTime 按浏览者的本地时间渲染一个 RFC 3339 时间戳。
 */
export function formatDateTime(
  value: string | null,
  fallback: string = UNKNOWN_TIME
): string {
  if (value === null) {
    return fallback;
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return fallback;
  }
  return parsed.toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/**
 * KeyState is a key's effective state, which is not the same as the `status`
 * column: the control plane never rewrites a row when a key's expiry passes,
 * so a key can be stored as "active" and be useless. A list that showed the
 * stored value alone would tell somebody a dead credential still works.
 *
 * KeyState 是一个 key 的实际状态，它与 `status` 列不是一回事：控制面在 key 过期时
 * 不会回头改写那一行，因此一个 key 可以存着 "active" 却已经没用了。只展示存储值的
 * 列表，会告诉别人一个已死的凭据仍然可用。
 */
export interface KeyState {
  kind: "active" | "revoked" | "expired" | "unknown";
  label: string;
}

/**
 * apiKeyState resolves a key's effective state at a given moment. The moment
 * is a parameter so the rule can be tested without waiting for one.
 *
 * apiKeyState 求解某一时刻一个 key 的实际状态。时刻是参数，好让这条规则无需等待
 * 就能被测试。
 */
export function apiKeyState(
  key: Pick<ApiKeySummary, "status" | "expiresAt" | "revokedAt">,
  now: Date
): KeyState {
  if (key.status === "revoked" || key.revokedAt !== null) {
    return { kind: "revoked", label: "已吊销" };
  }
  if (key.expiresAt !== null && Date.parse(key.expiresAt) <= now.getTime()) {
    return { kind: "expired", label: "已过期" };
  }
  if (key.status === "active") {
    return { kind: "active", label: "生效中" };
  }
  // An unfamiliar status is shown as it arrived. This build not recognizing a
  // value is something to see, not something to round off to "active".
  //
  // 不认识的状态原样展示。本次构建不认识某个值是一件需要被看见的事，而不是一件可以被
  // 约等于「生效中」的事。
  return { kind: "unknown", label: key.status };
}

/**
 * TTL_CHOICES is what the key creation form offers, in seconds.
 *
 * Zero is not among them: the API reads zero as "use the service default",
 * and a form that sent it would be asking for whatever the deployment happens
 * to be configured with while showing the person a specific number. Every
 * choice here is explicit, and "never" is the negative value the API reserves
 * for exactly that — not a hundred years.
 *
 * TTL_CHOICES 是创建 key 表单提供的选项，单位为秒。
 *
 * 零不在其中：API 把零读作「使用服务默认值」，而一个发送零的表单，等于在向部署索取它
 * 碰巧被配置成的那个值，却对当事人显示一个具体的数字。这里每个选项都是显式的，而
 * 「永不过期」用的是 API 为此保留的负值——不是一百年。
 */
export const TTL_CHOICES: readonly { value: string; label: string; seconds: number }[] = [
  { value: "30d", label: "30 天", seconds: 30 * 24 * 60 * 60 },
  { value: "90d", label: "90 天（默认）", seconds: 90 * 24 * 60 * 60 },
  { value: "180d", label: "180 天", seconds: 180 * 24 * 60 * 60 },
  { value: "365d", label: "365 天（上限）", seconds: 365 * 24 * 60 * 60 },
  { value: "never", label: "永不过期", seconds: -1 },
];

/**
 * DEFAULT_TTL_CHOICE matches the control plane's own default lifetime.
 *
 * DEFAULT_TTL_CHOICE 与控制面自身的默认生命期一致。
 */
export const DEFAULT_TTL_CHOICE = "90d";

/**
 * ttlSeconds resolves a form choice to the value the API takes.
 *
 * ttlSeconds 把表单选项解析成 API 接受的值。
 */
export function ttlSeconds(choice: string): number | null {
  return TTL_CHOICES.find((item) => item.value === choice)?.seconds ?? null;
}

/**
 * matchesQuery reports whether a row's searchable fields contain the query.
 *
 * It is deliberately dumb — a case-insensitive substring over fields the view
 * names — because it filters only what has already been loaded. Anything
 * cleverer would invite reading its result as an answer about the tenant, and
 * these endpoints return a bounded slice, not the whole table.
 *
 * matchesQuery 报告一行的可搜索字段是否包含查询词。
 *
 * 它刻意很笨——在视图点名的那些字段上做不区分大小写的子串匹配——因为它只筛选已经加载
 * 的部分。更聪明的做法会诱使人把它的结果读成关于整个租户的答案，而这些端点返回的是
 * 有界的一段，不是整张表。
 */
export function matchesQuery(fields: readonly string[], query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (needle === "") {
    return true;
  }
  return fields.some((field) => field.toLowerCase().includes(needle));
}

/**
 * AUDIT_ACTIONS is the set of actions the control plane writes, mirroring
 * `model.Action*`.
 *
 * The filter offers these rather than a free-text box because the API matches
 * an action exactly: a typed "apikey.revok" would return an empty page, which
 * reads as "nothing was revoked" — a wrong answer rather than no answer. An
 * action outside this list can still appear in the table; the list bounds what
 * can be filtered for, not what can be shown.
 *
 * AUDIT_ACTIONS 是控制面会写入的动作集合，镜像 `model.Action*`。
 *
 * 筛选提供的是这些选项而不是一个自由文本框，因为 API 对动作做精确匹配：手打的
 * "apikey.revok" 会返回空页，而那读起来像「没有任何东西被吊销过」——一个错误的答案，
 * 而不是没有答案。这个列表之外的动作依然可能出现在表格里；它限定的是可供筛选的范围，
 * 不是可供展示的范围。
 */
export const AUDIT_ACTIONS: readonly { value: string; label: string }[] = [
  { value: "user.login", label: "登录" },
  { value: "user.create", label: "创建用户" },
  { value: "apikey.create", label: "创建 API Key" },
  { value: "apikey.revoke", label: "吊销 API Key" },
  { value: "tenant.limits", label: "修改配额" },
  { value: "tenant.create", label: "创建租户" },
];

/**
 * QUOTA_DIMENSIONS describes the three limits a tenant has, so the form and
 * its copy come from one place.
 *
 * Zero is not "unset" — quota.Limits documents it as unlimited, and every
 * existing tenant got zeroes when the columns were added. A form that showed
 * an empty box for zero would invite somebody to type a number believing they
 * were filling in a blank, when they would be adding a limit that was not
 * there before.
 *
 * QUOTA_DIMENSIONS 描述一个租户的三项限制，好让表单与它的文案出自同一处。
 *
 * 零不是「未设置」——quota.Limits 把它记载为不限制，而且这几列被加上时，所有既有租户
 * 得到的都是零。一个把零显示成空白输入框的表单，会诱使人以为自己在填一个空缺而输入
 * 数字，实际上他是在添加一条原本不存在的限制。
 */
export const QUOTA_DIMENSIONS = [
  {
    field: "requestsPerMinute" as const,
    wire: "requests_per_minute",
    label: "每分钟请求数",
    help: "限制调用频率。令牌桶连续补充，不按整分钟重置。",
  },
  {
    field: "tokensPerMinute" as const,
    wire: "tokens_per_minute",
    label: "每分钟 Token 数",
    help: "限制工作量。在响应完成后扣减，因此超大的单次请求会被放行，超支由其后的请求偿付。",
  },
  {
    field: "maxConcurrent" as const,
    wire: "max_concurrent",
    label: "最大并发数",
    help: "限制同时进行的请求数，保护容量而不是公平性。",
  },
] as const;

/**
 * formatLimit renders one quota dimension. Zero is spelled out as unlimited
 * rather than shown as "0", which a reader would otherwise take for "no
 * requests allowed" — the opposite of what it means.
 *
 * formatLimit 渲染一个配额维度。零会被写成「不限制」而不是显示为 "0"，否则读者会把它
 * 理解成「一次请求都不允许」——与它的实际含义正好相反。
 */
export function formatLimit(value: number): string {
  return value === 0 ? "不限制" : value.toLocaleString("zh-CN");
}

/**
 * limitProblem validates one quota input against what the control plane
 * accepts: a non-negative integer.
 *
 * An empty box is refused rather than read as zero. Zero means unlimited here,
 * so silently substituting it for "I left this blank" would remove a limit
 * somebody never meant to touch — and this endpoint writes the whole set at
 * once, so every blank is submitted along with the field they did edit.
 *
 * limitProblem 按控制面接受的形式校验一个配额输入：非负整数。
 *
 * 空输入框会被拒绝，而不是被读作零。这里零表示不限制，因此把「我没填」悄悄换成零，
 * 会移除一条当事人从未打算碰的限制——而且这个端点是整组一次性写入的，所以每一个空白都
 * 会连同他确实改过的那个字段一起被提交。
 */
export function limitProblem(raw: string): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return "请填写数值；不限制请填 0。";
  }
  if (!/^\d+$/.test(trimmed)) {
    return "只能填写非负整数。";
  }
  if (!Number.isSafeInteger(Number(trimmed))) {
    return "数值超出可表示范围。";
  }
  return null;
}

/**
 * TERMINAL_JOB_STATES are the states a run cannot leave. They mirror
 * runtime.Workflow* — a run in one of these is finished, and the Gateway will
 * answer it from its own table without asking the node again.
 *
 * TERMINAL_JOB_STATES 是运行无法再离开的状态。它们镜像 runtime.Workflow* ——处于其中
 * 之一的运行已经结束，Gateway 会直接用自己的表作答，不会再去问节点。
 */
export const TERMINAL_JOB_STATES: readonly string[] = [
  "succeeded",
  "failed",
  "cancelled",
];

/**
 * isTerminalJobState reports whether a state is final.
 *
 * An unfamiliar state is treated as non-terminal, which is the cautious
 * direction: a state this build does not know may still be moving, and
 * presenting it as settled would be a claim nobody made.
 *
 * isTerminalJobState 报告一个状态是否是终态。
 *
 * 不认识的状态按非终态处理，那是谨慎的方向：本次构建不认识的状态可能仍在变化，把它
 * 呈现为已尘埃落定，是在作出一个没人作过的断言。
 */
export function isTerminalJobState(state: string): boolean {
  return TERMINAL_JOB_STATES.includes(state);
}

/**
 * observationAge describes how long ago a non-terminal state was observed.
 *
 * The Gateway learns a run advanced only while its submitter is asking — see
 * common/workflowview.Job.State. So a run shown as "running" says two things,
 * and only one of them is certain: that it was running when somebody last
 * looked, and that nobody has looked since. This renders the second one, which
 * is the part a reader would otherwise supply themselves, wrongly.
 *
 * observationAge 描述一个非终态是多久之前被观测到的。
 *
 * Gateway 只在提交方还在询问时才知道运行有了进展——见 common/workflowview.Job.State。
 * 因此一个显示为「运行中」的运行说了两件事，而其中只有一件是确定的：上次有人看的时候
 * 它在运行，以及从那以后没人再看过。这里渲染的是第二件——否则读者会自己去补，而且补错。
 */
export function observationAge(
  observedAt: string,
  now: Date
): { minutes: number; label: string } | null {
  const observed = Date.parse(observedAt);
  if (Number.isNaN(observed)) {
    return null;
  }
  const minutes = Math.floor((now.getTime() - observed) / 60_000);
  if (minutes < 1) {
    return { minutes: 0, label: "刚刚" };
  }
  if (minutes < 60) {
    return { minutes, label: `${minutes} 分钟前` };
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return { minutes, label: `${hours} 小时前` };
  }
  return { minutes, label: `${Math.floor(hours / 24)} 天前` };
}
