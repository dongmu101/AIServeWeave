# 按官方价 × 倍率计费（参照 sub2api）

Status: needs-triage

Triage（2026-11-16）：参照 [sub2api](https://github.com/Wei-Shaw/sub2api) 的 token 计价方式。已确定：

- 单价来自内置的官方价目表（LiteLLM `model_prices_and_context_window.json`），运维可以逐个别名覆盖。
- 售价 = 基础价 × 模型倍率 × 方案系数。租户的档位折扣用「价格方案」表达。
- 1 点 = 1 美元。
- 价目表同步时：新增条目自动生效；已有条目改价超过阈值或被删除时，等运维审核。
- 本期计费维度：输入、输出、缓存读取、缓存写入（分 5 分钟与 1 小时）、推理输出、长上下文分档。
- 本期配套：每次请求的费用明细；余额账目中的用量改为按天封账合并，账目只追加、不修改（见「余额账目」）。

不在本期，但数据结构已预留（见「以后的扩展」）：Key 额度与 5h/1d/7d 限额、订阅套餐、priority/flex/batch 服务等级、音频与图片 token、按次或按张计费、预扣、成本与毛利。

复查记录（2026-11-16）：第一版把四种 token 写死成字段、价目表同步后立即生效、按入账时间定价、倍率分散在三处，并且缺了长上下文分档与 1 小时缓存价。这些问题会造成少收，每加一种计价维度也都要同时改协议和表结构。本版按下面的原则重写。

复查记录二（2026-11-16）：`usage_charges` 每个请求一行，不分区也不清理，增长速度与 `usage_records` 相同。改为由按月分区的 `usage_records` 承担扣费防重，`usage_charges` 停止写入，见「扣费防重并入 `usage_records`」。

复查记录三（2026-11-16）：`credit_ledger` 每次推送、每个租户写一条用量账目，同样增长过快，而且作为财务记录不能按时间删除。改为累计在 `tenant_billing` 上、按平台时区每天封账时才写入一条账目，账目只追加，见「余额账目」。

## 设计原则

1. **计价维度不写死**。用量和单价都用「计量项」表达，新增维度不改表结构，也不改推送协议。
2. **新组件先上线也不会少收**。明细是总数的一部分，控制面不认识的计量项按所属总数的基础价收费。
3. **计价可以复现**。价格有版本，计价按请求发生的时间取价，结果与哪个副本入账、何时入账无关；每笔都记下所用的单价。
4. **上游数据不能直接信任**。价目表的改价和删除要经过审核，上游删掉的条目照样保留。
5. **计价逻辑只有一份**。写成不依赖数据库的纯函数包 `common/pricing`，控制面入账用它，以后 Gateway 估价、预扣也用它。

## 现状

按 token 扣费已经实现（见 [控制面 STATUS「计费与充值」](../../service/aiServeWeaveControlPlane/STATUS.md#计费与充值)）：

- `model_prices` 按别名设置每百万 token 的输入价、输出价，`*` 是默认价，全部由运维手动填写。
- 控制面在用量入账的同一个事务里计价，写入 `usage_charges` 并扣除 `tenant_billing.balance_micro`。
- prepaid 租户余额 ≤ 0 时，Gateway 返回 402。
- `runtime.Usage` 只有 `PromptTokens`、`CompletionTokens`、`TotalTokens`、`ReasoningTokens`。Anthropic 转换和 Agent 的 CLI runtime 都读到了缓存 token，但只是并进了输入总数（`common/runtime/anthropic/convert.go`、`service/aiServeWeaveAgent/cliagent/claudestyle.go`）。
- 控制面解析用量批次时拒绝未知字段（`handler/handlers.go` 的 `decodeUsageRecords`），所以推送协议每加一个字段，都必须先升级控制面。

## 术语

| 术语 | 含义 |
| --- | --- |
| 计量项 | 一种可计价的用量，例如 `input.cache_read`。见下节 |
| 价目表 | 从上游同步来的官方单价，按上游模型名索引 |
| 基础价 | 某个别名在倍率之前的单价表，来源依次是覆盖价、官方价、默认价 |
| 模型倍率 | 按别名设置、对所有租户都生效的倍率，默认 1 |
| 价格方案 | 一组折扣：默认系数，加上按别名的系数或固定售价。每个租户属于一个方案 |
| 方案系数 | 租户所属方案对某个别名的系数 |
| 售价 | 实际扣费所用的单价 = 基础价 × 模型倍率 × 方案系数，或方案里的固定售价 |

这几个术语在实现时同步加进 `GLOSSARY.md`。

## 计量项

### 命名与层级

计量项名称由小写字母、数字、下划线组成，以 `.` 分层，最长 64 字节。分两类：

- **token 计量项**：根是 `input` 或 `output`。`input` 的数量就是 `prompt_tokens`，`output` 的数量就是 `completion_tokens`。
- **数量计量项**：根是其他名称，例如以后的 `request`、`image`、`web_search_call`、`video_second`。本期没有。

**子项是父项的一部分**。例如 `input = 1000`、`input.cache_read = 600`，表示 1000 个输入 token 里有 600 个命中缓存。计价时，每个计量项按「自身数量 − 直接子项数量之和」计费：

```
input = 1000, input.cache_read = 600, input.cache_write = 300, input.cache_write.1h = 100

input                 按 1000 − 600 − 300 = 100 计
input.cache_read      按 600 计
input.cache_write     按 300 − 100 = 200 计（即 5 分钟写入）
input.cache_write.1h  按 100 计
```

子项之和大于父项时（上报数据不一致），把父项提高到子项之和，同时记一次指标。宁可多收，也不少收。

**控制面不认识的计量项**：单价查找会逐级回落到父项（见「单价查找」），所以它的数量最终按父项的单价收费，结果等于没有拆出这个子项。新计量项因此可以先由 Agent 和 Gateway 上报，控制面以后再配上单价，中间这段时间最多是少打一点折扣，不会少收。根本身不认识的数量计量项记 0 价，`price_source = none`，并在 Console 提示。

### 本期计量项

| 计量项 | 来源 |
| --- | --- |
| `input` | `prompt_tokens`（包含所有缓存读取与缓存写入） |
| `input.cache_read` | Anthropic `cache_read_input_tokens`；OpenAI Chat `prompt_tokens_details.cached_tokens`；Responses `input_tokens_details.cached_tokens`；Claude Code / Codex 等 CLI 输出中的对应字段 |
| `input.cache_write` | Anthropic `cache_creation_input_tokens`（5 分钟与 1 小时的合计）；CLI 输出中的对应字段 |
| `input.cache_write.1h` | Anthropic `cache_creation.ephemeral_1h_input_tokens` |
| `output` | `completion_tokens`（包含推理） |
| `output.reasoning` | 现有的 `ReasoningTokens` |

### 上报规范

各个 adapter 负责把上游语义统一成：

- `prompt_tokens` 包含全部输入，包括缓存读取、缓存写入，以及以后的图片和音频输入。Anthropic 的 `input_tokens` 不含缓存，要加回去（现有转换已经这样做）。
- `completion_tokens` 包含全部输出，包括推理。上游如果把推理单独列出、不计入输出（例如 Gemini 的 `thoughtsTokenCount`），也要加回去。
- 明细只填上游确实报告的值，不做估算。

## 单价

### 单价的键与单位

一条单价由 `(计量项, 变体, 档位)` 决定：

- **变体**：本期只有空字符串。以后用来表示 `priority`、`flex`、`batch` 等服务等级，取自用量记录的属性。
- **档位**：`above_input_tokens`，0 表示基础档。本次请求的输入总数（`input`）**大于**某个档位时，整次请求的所有计量项都改用该档单价；有多个档位时取适用的最高一档。

单价统一用「micro 点 / 百万单位」表示（token 计量项就是每百万 token），存为整数。1 点 = 1 美元，所以 $3 / 百万 token 就是 `3_000_000`。

### 单价查找

一张单价表（基础价或固定售价）中，计量项 `m` 在档位 `t` 下的单价，依次查找：

1. `m` 在 `t` 档的单价；
2. `m` 在低于 `t` 的最高一档的单价，直到基础档；
3. 把 `m` 换成父项，重复 1–2，直到根。

根也找不到单价时，该计量项单价为 0，`price_source` 记为 `none`。

价目表在导入时会补齐各档缺少的子项单价（见下节），所以第 2 步只对运维手填的单价表有意义。

## 价目表

### 字段映射

| LiteLLM 字段 | 计量项 | 档位 |
| --- | --- | --- |
| `input_cost_per_token` | `input` | 0 |
| `output_cost_per_token` | `output` | 0 |
| `cache_read_input_token_cost` | `input.cache_read` | 0 |
| `cache_creation_input_token_cost` | `input.cache_write` | 0 |
| `cache_creation_input_token_cost_above_1hr` | `input.cache_write.1h` | 0 |
| `output_cost_per_reasoning_token` | `output.reasoning` | 0 |
| 上述字段加 `_above_<N>k_tokens` 后缀 | 同上 | `N × 1000` |
| `long_context_input_token_threshold` 加 `long_context_input_cost_multiplier` / `long_context_output_cost_multiplier` | `input`、`output` 的所有计量项乘以对应倍率 | 阈值 |

- 单价从「USD / token」乘以 1e12 换成「micro 点 / 百万 token」，四舍五入成整数。
- **补齐档位**：某档只给了 `input`、`output` 的单价，没给子项（例如没有 `cache_read_input_token_cost_above_200k_tokens`）时，子项按「基础档子项单价 × 该档根单价 / 基础档根单价」补齐，并标记为推算值。不补齐的话，长上下文请求的缓存部分会按基础档收费。
- 其他字段（priority、flex、batches、音频、图片、搜索、按秒计价等，上游一共有 52 种计价字段）本期不导入。**每个条目的原始 JSON 照样保存**，以后要支持新维度时只需改映射、重新导入，不必重新抓取，也不必改表。
- 只收录 `mode` 为 `chat`、`completion`、`responses`、`embedding`，且 `input` 或 `output` 至少一个单价大于 0 的条目。全为 0 的条目（例如 `ollama/*`）不收录，以免本地模型被标成免费。
- 本机连不上 GitHub raw，目前只读到 sub2api 整理过的副本（218 个条目）。实现前须用上游原始文件核对字段名，`long_context_*` 这组字段也可能是 sub2api 自己加的。解析器对两种写法都支持，不认识的字段忽略。

### 同步与审核

- **来源**：远端 URL，或内嵌在控制面的快照（`go:embed`，用脚本 `scripts/update-price-catalog.sh` 更新）。来源做成接口，以后可以加入 OpenRouter 模型接口或自行维护的 JSON。
- **首次导入**：价目表为空时，直接导入内嵌快照并立即生效。之后只有在关闭远端同步（`RefreshInterval: 0`）时，升级带来的新快照才走下面的比对流程。
- **比对**：把新数据与当前生效的价目表逐个条目比较。
  - 新条目：自动生效。
  - 已有条目的任何一条单价出现以下情况时，整个条目进入待审核：变为 0；被删除；相对变动超过 `AutoApplyMaxChangePercent`。新出现的单价（例如原本没有 1 小时缓存价），与它原先回落到的单价比较。其余变动自动生效。
  - 上游删除的条目：进入待审核，标记为「上游已删除」，在运维确认之前继续使用原价。
- **审核**：运维逐条批准或拒绝。批准后从批准时刻起生效。拒绝后保留原价，上游内容不变就不再提醒。
- 自动生效、批准、拒绝都写审计 `billing.catalog.*`，记录条目名称与单价变化。
- **多副本**：各副本独立拉取，写入时用同步状态行上的内容哈希做比较并交换，同一份上游内容只有一个副本会写入。
- 拉取失败时保留现有数据，记录错误与指标，计费不受影响。单次下载限 30 秒、20 MB，JSON 解析失败也视为拉取失败。

### 配置

`Billing.PriceCatalog`：

| 键 | 默认 | 含义 |
| --- | --- | --- |
| `URL` | LiteLLM 在 GitHub raw 上的地址 | 远端价目表。中国大陆常连不上 GitHub raw，可以换成镜像 |
| `RefreshInterval` | `24h` | 远端同步间隔；`0` 表示不拉远端 |
| `AutoApplyMaxChangePercent` | `30` | 已有条目改价时，自动生效的最大相对变动；`0` 表示所有改价都要审核 |

## 定价与价格方案

### 基础价

别名 `A` 的基础价来源，按顺序取第一个存在的：

1. **覆盖价**：运维为 `A` 填写的单价表。它整张替换官方价，表里没填的计量项按「单价查找」逐级回落，不会再去价目表里找。Console 提供「从官方价复制」作为起点。
2. **官方价**：价目表条目。条目名取运维为 `A` 指定的「参照模型」；没有指定时用 `A` 本身，名称必须完全一致才算命中。不做模糊匹配，因为本地模型（例如 `deepseek-r1:7b`）被模糊匹配到 API 价时会按 API 价收费。
3. **默认价**：运维填写的默认单价表（取代现在的 `*` 行）。
4. 都没有：单价为 0，`price_source = none`。

### 模型倍率与价格方案

```
售价 = 基础价 × 模型倍率 × 方案系数
方案系数 = 租户所属方案中该别名的系数，没有就用方案的默认系数
```

- **模型倍率**：按别名设置，对所有租户生效，默认 1。用于「这个模型上游成本高，统一加价」。
- **价格方案**：包含默认系数，以及按别名设置的「系数」或「固定售价」。设置了固定售价时，该方案下这个别名直接按这张单价表收费，不再乘以模型倍率和方案系数。
- **默认方案**：系统内置，不能删除。它的默认系数就是全局倍率。没有指定方案的租户都属于默认方案。
- 还有租户在用的方案不能删除（返回 409）。
- 倍率与系数都以百万分之一为单位存成整数（`1_000_000` 表示 1 倍），范围 0–100 倍，0 表示免费。

示例：模型 X 的官方价是 $3 / 百万输入 token，模型倍率 1.5；VIP 方案的默认系数是 0.8。VIP 租户的售价就是 3 × 1.5 × 0.8 = $3.6 / 百万 token。

### 版本

为了按请求时间计价，以下三类数据都保留历史：

| 数据 | 保存方式 |
| --- | --- |
| 价目表单价 | 每行带 `valid_from` / `valid_to`；改价时关闭旧行、插入新行 |
| 定价配置（参照模型、覆盖价、模型倍率、默认价、全部价格方案） | 每次修改追加一个完整版本 `pricing_revisions`（JSON 文档），带生效时间。运维的修改接口须带上所基于的版本号，版本已变化时返回 409 |
| 租户所属方案 | `tenant_price_plans` 每行带 `valid_from` / `valid_to` |

另有一个全局的定价版本号，上述任何数据变化时加一。控制面在扣费事务里读取这个版本号，与进程内缓存不同时就重新加载，所以各副本永远用最新的价格数据计价，不需要等定时刷新。

## 单笔计价

对一条用量记录（别名 `A`、租户 `T`、请求时间 `t`、输入 / 输出总数、明细）：

1. 整理计量项：把 `ReasoningTokens` 并入 `output.reasoning`，校正父项不小于子项之和。
2. 取时间 `t` 时生效的定价配置、价目表与 `T` 所属的方案。`t` 晚于当前时间时按当前时间；`t` 早于最早的版本时（升级前发出的请求），用最早的版本。
3. 按「定价与价格方案」决定单价表、价格来源，以及总倍率（模型倍率 × 方案系数；方案固定售价为 1 倍）。
4. 按输入总数选出档位。
5. 每个计量项按「自身数量 − 子项数量之和」查出单价，算出费用。
6. 汇总：

```
精确原价 = Σ 计费数量 × 单价                       （micro 点 × 单位 / 百万）
原价 base_cost_micro = ceil(精确原价 / 1e6)
实扣 cost_micro      = ceil(精确原价 × 总倍率_ppm / 1e12)
```

- 用不限位宽的精确整数运算，结果超出 int64 时饱和为最大值，所以上报的数量再大，也不会让请求变成免费。非零用量在非零单价、非零倍率下，费用不会是 0。
- 每个计量项的费用单独向上取整，只用于展示，加总可能与 `cost_micro` 相差几个 micro。扣费以 `cost_micro` 为准。
- **商户自有节点**：照样计算并记录明细，但 `cost_micro = 0`，`charged = false`，与现行规则相同。
- **扣费模式**：只有 prepaid 租户真正扣余额。exempt 与 account_monthly 租户照样计算、记录费用，但不扣费，与现行规则相同。

每笔记录的明细（存在 `usage_records.details`，带格式版本号，以便以后演进）：

```json
{
  "v": 1,
  "rev": 42,
  "plan": "plan_vip",
  "src": "catalog",
  "ref": "claude-sonnet-x",
  "tier": 200000,
  "rate": 1200000,
  "attrs": {},
  "m": [
    {"k": "input",            "q": 250000, "x": 50000,  "p": 6000000, "c": 360000},
    {"k": "input.cache_read", "q": 200000, "x": 200000, "p": 600000,  "c": 144000},
    {"k": "output",           "q": 3000,   "x": 3000,   "p": 22500000, "c": 81000}
  ]
}
```

其中 `q` 是上报数量，`x` 是计费数量，`p` 是所用基础单价，`c` 是这一项的实扣费用；`src` 可以是 `override`、`catalog`、`default`、`plan_fixed`、`none`。`ref`（价目表条目名）只对运维显示，不向租户透露别名背后对应哪个上游模型。计费关闭时只记录 `v` 与 `m` 中的数量。

## 余额账目（`credit_ledger`）

### 问题

现在扣费事务每处理一批推送，就给批内每个租户写一条 `usage` 账目（`ref` 为「N requests」）。Gateway 每个副本每 5 秒推送一次，持续有流量的租户，每个副本每天最多 17,280 条。以 1000 个全天都有流量的租户、3 个 Gateway 副本计，每天最多约 5000 万行。

- 账目是财务记录，要长期保存，不能像用量记录那样分区后整段删除。
- 租户的「账目」页会被用量条目淹没，充值、退款反而找不到。
- 逐笔用量已经记在 `usage_records` 里，逐批的用量账目只是重复记录。

退款按当前余额计算（`logic/refunds.go`），没有任何逻辑去加总账目，`balance_after` 只用于显示。所以改变用量账目的粒度，不影响退款与扣费逻辑。

### 考虑过的做法

| 做法 | 结论 |
| --- | --- |
| 用量不进账目，只改余额 | 余额链会断：两条账目之间余额变了，却没有记录，无法对账。不采用 |
| 账目分区，定期删除旧的用量账目 | 财务记录不该删；删掉后，旧时段的余额链就不完整。不采用 |
| 后台定期把连续的用量账目压缩合并 | 仍然先写出大量行，再改写历史财务记录，比较复杂。只用作处理存量数据的可选命令 |
| 未封账的那条账目持续累加（复查前的方案） | 账目会被修改：以 `(created_at, id)` 为游标的增量同步会漏掉更新，查不到某个时刻账上原本写的是什么，也不能在数据库层面禁止修改账目。不采用 |
| **封账时才写入用量账目** | 采用，规则如下 |

### 规则

- **账目只追加，写入后不再修改。** 累计中的用量不写进账目，而是记在 `tenant_billing` 的累计字段上：累计金额、请求数、第一笔与最后一笔扣费时间、封账期限。余额照常每批扣减，所以拦截与退款都不受影响。
- **扣费时**（租户计费行已加锁）：累计已过封账期限，就先封账；然后扣余额、把本批加进累计。第一次累计时，封账期限设为平台时区的下一个午夜。
- **封账**：用累计值写入一条用量账目。金额是累计金额的负数，`balance_after` 是封账时的余额，`period_start` / `period_end` 是第一笔与最后一笔扣费时间，并写入 `request_count`。然后清空累计。
- **三个时机会封账**：
  1. 扣费时发现累计已过封账期限；
  2. 写入其他任何类型的账目（充值、退款、争议、调整、赠送等）**之前**，在 `applyCredit` 里、同一个事务内执行；
  3. 后台封账任务每分钟扫描累计已过期限的租户（`tenant_billing` 上按封账期限建索引）。没有它的话，租户停止使用后，累计值会一直不封账。
- **余额链**：第 2 条保证封账账目一定写在下一笔资金事件之前，所以按时间排列时，每一条都满足「上一条的 `balance_after` + 本条金额 = 本条的 `balance_after`」，并且「最后一条的 `balance_after` − 累计中的金额 = 当前余额」。中途有充值也接得上：

  ```
  11/16 08:03–10:30  用量    −3.00  余额  97.00  (1,204 次请求)
  11/16 10:30        充值  +100.00  余额 197.00
  11/16 10:31–23:58  用量    −2.00  余额 195.00  (861 次请求)
  11/17 进行中        用量    −0.40  （尚未封账，不在账目表中）
  ```

- **账目时间严格递增**：每个租户的新账目，`created_at` 取「当前时间」与「上一条账目时间 + 1 微秒」中较晚的一个（`tenant_billing` 记录上一条账目的时间）。封账账目与紧接着写入的充值账目即使在同一微秒发生，排序也不会颠倒；控制面副本之间的时钟误差也不会造成倒序。
- **并发**：扣费、其他账目与封账任务都在租户计费行锁下执行，多个副本同时运行也安全；封账任务加锁后会重新检查，另一个副本已经封账的就跳过。
- **数量**：每个租户每天最多一条用量账目，中途每有一笔资金事件就多切出一段。以 1000 个全天都有流量的租户计，每年约 37 万行。账目表继续不分区、永久保存。
- **只追加的防护（可选）**：控制面不会 UPDATE 或 DELETE `credit_ledger`，所以生产运行账号可以只授予这张表 INSERT 与 SELECT 权限；存量压缩命令改用 `Database.MaintenanceDSN` 执行。

### 配置

| 键 | 默认 | 含义 |
| --- | --- | --- |
| `Billing.LedgerTimezone` | `UTC` | 用量账目的日界所用的 IANA 时区，例如 `Asia/Taipei`。名称无效时拒绝启动。控制面引入 `time/tzdata`，不依赖镜像里有没有时区数据。修改只影响之后新开的累计，已经在累计的仍按原定期限封账 |

### 显示

- 账目接口另外返回 `pending_usage`（累计金额、请求数、起止时间）。Console 把它显示在账目页最上面的「进行中」一行，并注明尚未封账。
- 用量账目显示时间段（`period_start`–`period_end`）和请求数，并链接到用量页这段时间的明细。账目按扣费时间分段，用量页按请求时间筛选，两者的边缘可能差几秒，页面上注明。
- `ref` 照旧写「N requests」，旧版 Console 也能显示。
- 运维的租户列表新增「未封账用量」。

### 存量数据

- 升级前写入的逐批用量账目保留不动。升级后累计从 0 开始，余额链照常接续。
- 可选命令 `-migrate compact-usage-ledger`：把同一个租户、平台时区同一天内、中间没有其他类型账目的连续用量账目，合并成一条（金额相加，`balance_after` 取最后一条，`request_count` 由各条的 `ref` 加总，时间段取首尾）。合并前后余额链保持一致。按租户分批执行，可以中断，也可以重复执行。这是唯一会改写账目的操作，使用 `Database.MaintenanceDSN`，执行前须先备份。

## 上报链路

| 环节 | 变更 |
| --- | --- |
| `common/runtime.Usage` | 新增 `Details map[string]int64`（计量项名称 → 数量）与 `Attributes map[string]string` |
| Anthropic Messages（流式与非流式） | 填写 `input.cache_read`、`input.cache_write`、`input.cache_write.1h` |
| OpenAI Chat、Responses（流式与非流式） | 填写 `input.cache_read`；推理沿用 `ReasoningTokens` |
| 隧道协议 `Usage` | 新增 `map<string, int64> details = 5`、`map<string, string> attributes = 6` |
| Agent：Claude Code、Codex、Cursor 等 CLI runtime，远端供应商 runtime | 上游有报告时填写明细，不再只是并进输入总数 |
| Gateway 推送 `/internal/v1/usagerecords` | 记录新增 `details`、`attributes` 两个对象，为空时省略 |

- `attributes` 本期没有生产方，只是先放进协议，供以后的服务等级、批处理等使用，免得到时又要控制面先升级一轮。
- 控制面校验：每条记录最多 32 个明细、8 个属性；名称须符合计量项命名规则；数量不能为负；属性值最长 64 字节。不符合时整批拒绝，与现有的严格校验一致。
- **Gateway 推送前按同样的规则过滤**：不合规的明细或属性直接丢弃，并记录指标，记录本身照常推送。否则 Agent 只要有一个 bug，就会让整批用量被拒绝，造成漏记漏扣。
- 这两个对象加进协议以后，新增计量项不再需要改协议，也不需要控制面先升级。
- 旧 Agent 不上报明细，全部按 `input` / `output` 的基础价收费，与现在相同。

## 数据变更

迁移要同时提供 PostgreSQL 与 MySQL 版本，并同步 `sql/init.sql`。

### `usage_records/0004_pricing.sql`

新增：

| 列 | 说明 |
| --- | --- |
| `cache_read_tokens`、`cache_write_tokens` | `BIGINT NOT NULL DEFAULT 0`。从明细里抽出来的常用数量，供汇总求和（`cache_write_tokens` 含 1 小时写入）。其他计量项只在逐笔明细里看得到，以后需要汇总时再抽成列 |
| `cost_micro`、`base_cost_micro` | `BIGINT NOT NULL DEFAULT 0` |
| `rate_ppm` | `BIGINT NOT NULL DEFAULT 0` |
| `price_source` | `VARCHAR(16) NOT NULL DEFAULT ''`。升级前的记录为空，Console 显示为「升级前」 |
| `details` | `TEXT NULL`，即上面的明细 JSON |
| `charged` | `BOOLEAN NULL`。是否实际扣了余额。计费关闭时写入的记录与升级前的记录为 NULL |

**修订原有约定**：控制面 STATUS 原本规定「费用写进独立的 `usage_charges`，不给 `usage_records` 加列」，理由是不让未开计费的部署承担 schema 变更。但 `usage_records/0003` 已经加过列，而且缓存 token 本来就要加进这张表，这个理由已经不成立。费用和用量记录生命周期相同、分区相同、保留期相同，放在一起后：

- 用量汇总只需扫描一张表，不用逐行回查 `usage_charges`；
- 逐笔明细跟着 `usage_records` 一起按月分区、400 天后清理。原设计要往 `usage_charges` 加 8 列，而这张表不分区、也不清理。

### 扣费防重并入 `usage_records`

**问题**：`usage_charges` 每个计费请求写一行（exempt 租户也写），行数与 `usage_records` 一比一，却既不分区，也不清理。每行连同两个索引约 200–300 字节。以 200 个活跃的 Claude Code 用户、每人每天 1000 次请求估算，每天约 20 万行，一年约 7300 万行、15–20 GB，并且一直累积。

- 插入本身不会变慢：request id 是 UUIDv7，按时间递增。
- 但备份、恢复和以后任何 `ALTER` 都会越来越慢。
- `(tenant_id, created_at)` 索引没有任何查询在用，只增加写入量。

当初不分区，是因为分区表的主键必须包含分区列，这样 `request_id` 就无法单独唯一，而扣费防重依赖它（见根 STATUS 2026-11-14「不分区」）。如果把它直接改成分区表，首次迁移要整表复制，期间表不可写，用量入账会被卡住，Gateway 的有限缓冲可能丢记录；转换完以后，仍然是两张一比一的大表。

**做法**：不再单独维护这张表，改由 `usage_records` 防重。

- 扣费事务的顺序改为：
  1. 按 id 顺序锁定本批租户的计费行（与现在相同）；
  2. 在 `usage_records` 中查找本批 id 是否已经存在（`WHERE id IN (...)`，不带 `created_at`，所以会查遍所有分区）；
  3. 只插入不存在的记录，同时写入费用与 `charged`；
  4. 扣余额、写账目。
- **防重强度不变**。prepaid 租户的检查在租户行锁之下进行，与现在相同。查询不带 `created_at`，所以即使重发时 `created_at` 变了也查得到。唯一的差别是，唯一性从数据库约束变成「锁内检查 + 分区内主键」。
- 没有计费行的租户不加锁，但它们不扣费，并发重复最多让报表多一行，这和现在的 `usage_records` 一样。
- **查遍分区的开销**：按月分区、保留 400 天约 14 个分区，每个 id 是 14 次主键查找。一批几百条、每 5 秒一批，可以接受。
- **保留期**跟随 `usage_records`（默认 400 天），整段删除过期分区。Gateway 的推送缓冲只保留几分钟到几小时，不会重发 400 天前的记录，所以删除后不会重复扣费。

**过渡**：

- 升级后 `usage_charges` 不再写入。升级前已入账、升级后又被重发的记录，在 `usage_records` 中已经存在（两者一直在同一个事务里写入），所以不会重复扣费。
- 计费关闭期间写入的记录，开启计费后再被重发，不会补扣（现在会补扣）。这种情况只可能出现在切换开关的那几秒，而且那段时间本来就不该收费。
- 升级前的费用只存在 `usage_charges` 里。提供一个可选的分批回填命令（例如 `-migrate backfill-usage-cost`，可以重复执行），在低峰期按分区把 `cost_micro` 与 `charged` 写进 `usage_records`。不回填时，这些记录在 Console 显示为「升级前」，也不计入汇总费用。
- `usage_charges` 本期保留不删，留给回填命令使用；下一个版本的迁移再删除。

### `billing/0012_pricing.sql`

| 表 | 说明 |
| --- | --- |
| `catalog_models` | 价目表条目：`model` 主键、`provider`、`mode`、原始 JSON、状态（`active` / `removed_upstream`）、被拒绝的上游内容哈希、时间 |
| `catalog_prices` | 价目表单价：`model`、`meter`、`variant`、`above_input_tokens`、`price_micro_per_m`、`derived`、`valid_from`、`valid_to` |
| `catalog_pending` | 待审核的变动：条目、类型（改价 / 删除）、拟采用的单价、上游内容哈希、发现时间 |
| `catalog_sync_state` | 单行：来源、内容哈希、条目数、同步时间、最后一次错误 |
| `pricing_revisions` | 定价配置版本：自增 `id`、`effective_at`、JSON 文档、`created_by`、备注 |
| `tenant_price_plans` | 租户所属方案：`tenant_id`、`plan_id`、`valid_from`、`valid_to` |
| `pricing_state` | 单行：全局定价版本号 |
| `credit_ledger`（加列） | `period_start DATETIME NULL`、`period_end DATETIME NULL`：用量账目的第一笔与最后一笔扣费时间，其他账目为空；`request_count BIGINT NOT NULL DEFAULT 0` |
| `tenant_billing`（加列） | 累计中的用量：`usage_acc_micro`、`usage_acc_count`（`BIGINT NOT NULL DEFAULT 0`）、`usage_acc_since`、`usage_acc_last`、`usage_acc_until`（`DATETIME NULL`，`usage_acc_until` 建索引供封账任务扫描）；上一条账目的时间 `last_ledger_at DATETIME NULL` |

`credit_ledger` 现在可能已经很大，须确认两种数据库上加列都是即时完成，不会整表复制。

迁移把现有的 `model_prices` 转换成第一个定价配置版本：每一行变成该别名的覆盖价（输入、输出），模型倍率 1；`*` 行变成默认价；再建立系数为 1 的默认方案。`model_prices` 表保留不删，以便回退时对照，但不再读写。

## API

### 运维（平台会话，`Billing.Enabled`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/operator/v1/billing/pricing` | 当前定价配置与版本号 |
| `PUT` / `DELETE` | `/operator/v1/billing/pricing/models` | 设置或删除某个别名的参照模型、覆盖价、模型倍率。别名可能含 `/`，所以放在请求体或 `?model=` 里。须带 `base_revision` |
| `PUT` | `/operator/v1/billing/pricing/default-prices` | 默认单价表 |
| `GET` / `POST` | `/operator/v1/billing/plans` | 价格方案列表与新建 |
| `PUT` / `DELETE` | `/operator/v1/billing/plans/:id` | 名称、默认系数，以及按别名的系数或固定售价。默认方案不能删除；还有租户在用的方案返回 409 |
| `PUT` | `/operator/v1/billing/tenants/:id/plan` | `{"plan_id": "..."}` 或 `{"plan_id": null}`（回到默认方案），返回 `204` |
| `GET` | `/operator/v1/billing/pricing/revisions` | 定价配置的历史版本，带操作人与差异摘要 |
| `GET` | `/operator/v1/billing/effective-prices` | 全部路由别名，加上已配置但没有路由的别名：价格来源、参照条目、各计量项与各档的基础价、模型倍率、售价。可选 `plan_id` |
| `GET` | `/operator/v1/billing/catalog` | 分页查询价目表（`q` 按名称前缀），附同步状态 |
| `POST` | `/operator/v1/billing/catalog/sync` | 立即同步 |
| `GET` | `/operator/v1/billing/catalog/pending` | 待审核的变动，附新旧单价对照 |
| `POST` | `/operator/v1/billing/catalog/pending/approve`、`/reject` | 请求体带条目名（可能含 `/`） |
| `GET` | `/operator/v1/usage/records` | 跨租户的逐笔明细，参数同租户版，另可带 `tenant_id`；包含 `ref` |

原来的 `/operator/v1/billing/model-prices` 移除，所以 Console 要和控制面一起升级。所有写操作都写审计。

### 租户（租户会话，所有角色可读）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/admin/v1/billing/prices` | 本租户已获授权的别名，按本租户的方案算出的各计量项、各档售价（`Billing.Enabled`） |
| `GET` | `/admin/v1/usage/records` | **新增**。逐笔明细，新的在前，游标分页，每页最多 200 条；可选 `from`、`to`、`model`、`key_id`。每行包含：`request_id`、`created_at`、`model`、`endpoint`、`key_id`、`user_id`、输入与输出总数、计量项明细。开启计费时另带 `cost_micro`、`base_cost_micro`、`rate_ppm`、`price_source`、`charged`、各计量项的售价与费用。不包含 `ref` |

所有字段都直接取自 `usage_records`，不需要关联其他表。

`/admin/v1/billing/ledger` 与 `/operator/v1/billing/ledger` 的每条账目新增 `period_start`、`period_end`、`request_count`（只有用量账目才有值）；响应另带 `pending_usage`（尚未封账的累计用量）。运维的租户列表新增未封账用量。

### 用量汇总

`/admin/v1/usage/summary` 与 `/operator/v1/usage/summary` 的每行新增 `cache_read_tokens`、`cache_write_tokens`，开启计费时另带 `cost_micro`。仍是同一张表的一次分组求和。控制面 STATUS 中「这两个端点只做用量汇总，不做定价」改为：汇总只读取入账时已算好的费用，不重新计价。

### 公开目录

`GET /billing/v1/catalog` 的模型价格改为：有路由的别名，按默认方案算出的各计量项、各档售价。不包含参照条目。

## Console

运维：

- **模型定价**：以别名为行，显示价格来源、参照条目、基础价（四类主要计量项，有长上下文档位时可以展开）、模型倍率、默认方案下的售价。编辑时可以搜索价目表选择参照模型，也可以「从官方价复制」后改成覆盖价。页面顶部显示默认价和价目表同步状态，附「立即同步」按钮。
- **价目表审核**：列出待审核的变动，显示新旧单价对照与变动幅度，可以逐条批准或拒绝。有待审核项时，计费页显示角标。
- **价格方案**：方案列表、默认系数、按别名的系数或固定售价、正在使用的租户数。
- **租户列表**：新增「价格方案」列，可以修改。
- **未定价提示**：有别名的价格来源为 `none`，或最近 24 小时内出现 `price_source = none` 的用量时，计费页显示横幅「有 N 个别名正在以 0 价计费」。

租户：

- **用量页**：合计卡片新增「消耗点数」；按模型、按用户、按 Key 三个标签页新增缓存 token 列和费用列；新增「明细」标签页，逐笔列出请求，可以展开查看各计量项的数量、单价与费用、档位、倍率。
- **API Key 页、用户页**：「Token 用量」列加上点数。
- **计费页**：新增「模型价格」分区，显示本租户的售价。「账目」最上面显示「进行中」的累计用量；用量条目显示时间段与请求数，并链接到这段时间的用量明细。

点数最多显示 6 位小数，单价显示为「点 / 百万 token」，旁边注明 1 点 = 1 美元。文案提供八种语言。租户可以看到原价与倍率（国内中转站的惯例）；以后如果需要隐藏，再加开关。

## 升级与兼容

升级顺序：**先控制面，再 Gateway，最后 Agent**。Console 与控制面一起升级。

- 推送协议新增 `details`、`attributes`，旧控制面会拒绝整批，所以控制面必须先升级。这是最后一次因为计量项而必须让控制面先升级。
- 旧 Gateway 与旧 Agent 不上报明细，计费结果与现在相同。

升级须知（行为变更）：

- 原本落到 `*` 默认价的别名，只要名称与价目表条目完全一致，升级后就按官方价收费。升级后先在「模型定价」页核对售价。
- 1 点 = 1 美元成为定价基准。现有余额与充值包的点数不变，运维须按这个单位重新核对充值包的价格。
- 已有的 `model_prices` 行变成覆盖价，模型倍率与默认方案系数都是 1，这些别名升级前后价格完全一致。
- 计价改为按请求时间取价：升级前就已发出、升级后才入账的请求，也按升级时生效的新配置计价（升级前没有版本历史）。
- 扣费防重改由 `usage_records` 承担，**不能直接换回旧版控制面**：旧版只查 `usage_charges`，升级期间入账的记录如果再被重发，会被重复扣费。回退必须恢复升级前的备份，与 2026-11-14 的分区迁移相同。
- 升级前的费用要运行回填命令后，才会出现在用量汇总与明细里。
- 用量账目改为按天封账后才写入。升级前的逐批用量账目保留不动；需要清理的部署，备份后用维护账号运行 `-migrate compact-usage-ledger`。
- 新增配置 `Billing.LedgerTimezone`（默认 `UTC`），台湾或大陆市场的部署应设为 `Asia/Taipei` 或 `Asia/Shanghai`。

## 以后的扩展（本期不做，确认没被设计堵死）

| 能力 | 怎么接进来 |
| --- | --- |
| priority / flex / batch 服务等级 | Gateway 在 `attributes` 填写 `service_tier`，单价的「变体」取这个值；导入价目表里对应的字段 |
| 音频、图片 token | 新增 `input.audio`、`input.image`、`output.image` 等计量项，补上 adapter 与字段映射，不改协议，也不改表 |
| 按次、按张、视频秒数、搜索次数 | 数量计量项，例如 ComfyUI 图像生成上报 `image`。数量计量项的单价上限要另外设定（现有上限按 token 设计） |
| Key 额度、订阅套餐的消费上限 | 沿用 `billing_blocked` 的做法：扣费事务累计各时间窗口的消费，Key 校验结果带拦截标记。超额时间差约为推送间隔 5 秒加校验缓存 5 秒，另加正在处理的请求 |
| 预扣 | 贵模型单次请求可能要几美元，现在只在余额 ≤ 0 时拦截，并发请求会透支。Gateway 可以用 `common/pricing` 估价预留，再由控制面结算 |
| 成本与毛利 | 同一个别名可能路由到自有 GPU、CLI 账号或远端 API，成本差很多。在明细 JSON 里加成本侧字段，按实际服务的目标计价 |
| 按小时汇总 | 数据量大到汇总查询变慢时，在扣费事务里累加按小时汇总的表，汇总改读这张表 |
| 隐藏原价与倍率 | 加一个配置开关，租户 API 只返回售价 |

## 验收要点

- **计价引擎**（`common/pricing` 单元测试）：子项扣减与父项校正、不认识的计量项回落、档位选择与查找顺序、补齐档位、覆盖价整张替换、默认价、固定售价、模型倍率 × 方案系数、倍率为 0、取整、溢出饱和、商户自有节点。
- **价目表**：两种长上下文写法、过滤规则、首次导入、新条目自动生效、超过阈值与降为 0 进入待审核、上游删除保留原价、批准与拒绝、拒绝后不再提醒、多副本同时同步只写一次、拉取失败保留原有数据。
- **版本**：改价前发出、改价后入账的请求按旧价计价；多副本在定价版本号变化后立即使用新价；修改接口的版本冲突返回 409。
- **扣费事务**：明细、费用列与 `charged` 正确写入；重复推送不重复扣费（沿用现有用例）；重发时 `created_at` 不同、或者落在另一个月的分区，也不重复扣费；升级前已入账的记录在升级后重发，不重复扣费；多副本并发推送同一批；回填命令可以重复执行、可以中断后继续。
- **余额账目**：新增共享 store 场景，检查每个租户的账目都满足「上一条 `balance_after` + 本条金额 = 本条 `balance_after`」，且最后一条等于当前余额。并且「最后一条 `balance_after` − 累计中的金额 = 当前余额」。覆盖：同一天内多批累加；跨过平台时区午夜后由下一次扣费封账；闲置租户由封账任务封账；当天中途发生充值、退款冻结与释放、争议、调整、赠送时先封账；扣费、封账任务与其他账目在多副本下并发；同一微秒内封账与充值、副本时钟倒退时账目时间仍严格递增；`Asia/Taipei` 与有夏令时的时区（如 `America/New_York`）的日界；修改时区只影响新开的累计；压缩命令前后余额链一致、可以重复执行。另需确认控制面代码中没有任何对 `credit_ledger` 的 UPDATE 或 DELETE。
- **上报链路**：Anthropic、OpenAI Chat、Responses（流式与非流式）以及 Claude Code、Codex CLI 的明细；隧道新旧版本互通；推送契约与校验上限。
- **迁移**：在临时 PostgreSQL 16 与 MySQL 9.7 上跑 live 用例，包括 `model_prices` 转换、保留升级前数据、分区表加列。`usage_records` 是按月分区的大表，须确认两种数据库上加列都是即时完成（MySQL `ALGORITHM=INSTANT`，PostgreSQL 常量默认值），不会整表复制。
- **Console**：单元测试、类型检查、lint；浏览器检查运维定价、方案、审核，以及租户明细页。

## 实施拆分（确认后写入 `issues/`）

1. **计量项上报**：`runtime.Usage`、各 adapter、隧道协议、Agent runtime、Gateway 推送；控制面接收、校验并写入 `usage_records/0004` 的明细与缓存列（这一步还不计价）。
2. **价目表**：解析与字段映射、补齐档位、带有效期的存储、内嵌快照、同步与比对、审核；运维价目表 API。
3. **定价配置与价格方案**：版本存储、`model_prices` 转换、方案与租户方案历史、全局定价版本号；运维定价与方案 API。
4. **计价引擎与扣费防重**：`common/pricing` 纯函数包；`chargeUsage` 改用它，写入费用列与明细；扣费防重改由 `usage_records` 承担，`usage_charges` 停止写入；回填命令。
5. **余额账目按天封账**：`tenant_billing` 与 `credit_ledger` 加列、扣费事务累加、`applyCredit` 先封账、账目时间严格递增、后台封账任务、`Billing.LedgerTimezone`、memstore；账目 API 的新字段与 `pending_usage`；压缩命令。
6. **查询 API**：汇总加列、逐笔明细、租户价格、公开目录、未定价提示。
7. **Console 运维页**：模型定价、价目表审核、价格方案、租户方案。
8. **Console 租户页**：用量页费用与明细、Key 页与用户页的点数、计费页价格与账目。
9. **文档**：控制面 STATUS「计费与充值」与用量账本两节、根 STATUS、GLOSSARY、部署配置示例。

依赖：1、2、3 互不依赖，可以并行；4 依赖 1、2、3；5 与 4 改的是同一个扣费事务，排在 4 之后；6 依赖 4；7 依赖 2、3；8 依赖 5、6。
