# Agent

节点侧 Agent 的配置与使用说明。交付范围与验收状态见 [STATUS](STATUS.md)，隧道协议见 [隧道 README](tunnel/README.md)，部署见 [Agent 部署指南](../../deploy/agent/README.md)。

平台 Agent 接入后默认不共享给租户。是否共享由运维在「Agent 接入」、审批确认或「Agent 共享设置」中控制，本机 YAML 和自报标签不能开启共享。非共享 Agent 的 Claude Code / Codex 账号仍可按独享商品单独出售，购买权限只覆盖对应账号。见[共享设置](../../docs/agent-sharing.md)。

## 配置文件

Claude Messages 文件与视频的转换工具、模型路径和限额可在 YAML 顶层 `attachments:` 配置；详见 [附件输入说明及完整配置](attachments/README.md)。这些配置仅本地使用，修改后需重启 Agent，设置页保存其他设置会保留它们；不自动安装转换程序或模型。

`-config` 指向一份 YAML 文件，包含远程 Gateway 连接（`gateway:` 段，对应 `-gateway`/`-registry`/证书三件套/`-allowed-runtimes`/`-labels`/`-max-gateways`）、本地运行时声明（`runtimes:`，是 `-ollama-url` 之外的补充而非替代），以及 `auto_discover`/`metrics_addr`/`log_level`。

- 命令行显式传入的 flag 始终优先于文件里的同名设置（`main.go` 的 `applyConfig`），文件只填补 flag 留在默认值上的空白，两者可以同时使用（如 `-node-id` 覆盖 `node_id`）。
- 不传 `-config` 时，`resolveConfigPath` 在当前工作目录找 `config.yaml`，存在就加载，不存在就只靠 flag 运行；文件存在但解析失败是致命错误，不会被当成「没找到」跳过。`-config-ui` 不带 `-config` 时也使用同一默认路径。
- YAML 经 `github.com/spf13/viper` 解码，依赖例外见 [依赖边界](../../docs/agents/dependencies.md#agent-本地-yaml-配置例外)。它与 `modelpull`/`agentupgrade` 的纯 JSON 本地清单不同源：这份文件给人手改或由设置页面生成。

完整示例：

```yaml
# 远程 Gateway 连接。种子列表只需一个可达即可，连接后由名册接管其余副本。
gateway:
  endpoints:
    - gw-1.example.com:8443
    - gw-2.example.com:8443
  registry: registry.example.com:9443
  node_id: node-mac-01               # 留空则由 Registry 分配
  cert_file: /etc/aisw/agent-cert.pem
  key_file: /etc/aisw/agent-key.pem
  ca_file: /etc/aisw/registry-ca.pem
  bootstrap_token_file: /etc/aisw/bootstrap-token   # 仅首次注册用，用后即删
  allowed_runtimes:                  # 留空表示放行本节点全部已配置的运行时
    - ollama-local
    - vllm-local
  labels:
    region: home-mac
    gpu: 4090
  max_gateways: 4

# 本地推理后端声明：可以声明任意数量、任意受支持种类（见 STATUS「后端适配」；CLI 不配置 base_url）。
runtimes:
  - id: ollama-local
    kind: ollama
    base_url: http://127.0.0.1:11434
  - id: vllm-local
    kind: vllm
    base_url: http://127.0.0.1:8000

auto_discover: true                  # 额外探测本机 Ollama/vLLM/SGLang 常见端口
auto_discover_interval: 30s
metrics_addr: 127.0.0.1:9091         # 留空关闭 /metrics
log_level: info
```

`allowed_runtimes` 是执行白名单：显式配置后，每个要对外服务的运行时 ID 都必须列入，改动后需重启 Agent。

## 本地设置页面

正常启动 Agent 时默认同时启动本地 Web 设置页面，访问 `http://127.0.0.1:8899`；`-config-ui-addr` 可改监听地址，只接受回环地址（`127.0.0.1`/`::1`/`localhost`）。页面拒绝非回环 Host（防 DNS 重绑定）与跨站 POST（`http.CrossOriginProtection`）。

- 左侧菜单分为 Gateway 连接、节点与认证、本地 AI、日志与指标四组；切换保留输入，保存按钮统一保存全部分组，写入 `-config` 指向的文件（未指定时为当前目录的 `config.yaml`）。禁用 JavaScript 时仍可通过锚点导航编辑所有分组。
- 本地 AI 是可添加、移除的多条配置，逐条选择后端类型并填写唯一运行时 ID；CLI 类后端不填服务地址，Codex / Claude Code 可填写账户目录（见[同一 CLI 的多个账户](#同一-cli-的多个账户)）。
- 保存后 `runtimes:` 立即重新加载（见[运行时热重载](#运行时热重载)），页面提示是否成功；Gateway 连接、证书、运行时白名单、指标与日志仍需重启 Agent。
- Gateway 连接分组显示当前实例的实时连接状态、已连接副本数与每个 Gateway 地址的状态，每五秒自动刷新，也可手动刷新；刷新不覆盖未保存的表单。仅配置模式与隧道未启用会明确标示；状态请求失败时清除旧状态，提示 Agent 可能已停止或正在重启。
- 本地 AI 分组按运行时列出当前发现的可用模型（真实模型 ID、状态与去重总数），「刷新模型」不丢失未保存的配置。列表来自 Agent 内存快照、只含健康运行时，只读，不调用推理或另起 CLI；本地可用不等于 Gateway 可调度，后者还受连接与白名单限制。
- 正常运行时页面启动失败（如端口被占用）只记录错误，Agent 继续运行。
- `-config-ui` 只运行设置页面，不启动隧道或运行时，页面提示没有运行中的模型目录，页面启动失败则退出。首次启动没有配置文件、且没有显式传入 `-config-ui`、`-config-ui-addr`、`-config`、`-gateway` 中任何一个时，自动进入这一模式以完成初始配置。

## 后端未就绪时的启动行为

本地声明的后端（本地推理服务、CLI 或托管供应商）暂时不可用时，Agent 继续启动，日志提示「运行时未就绪」，状态快照上报 `unhealthy` 及原因。此时它的模型目录为空，Gateway 不会调度到它；其他就绪后端照常服务。

Agent 默认每 10～11 秒重新执行身份探测和模型发现，两项都成功后自动标记为 `healthy` 并发布模型，无需重启；`-ollama-url` 同样适用。无效配置、重复运行时 ID、未注册类型或本地白名单拒绝仍会使启动失败；配置了 `-comfyui-managed-image` 而容器启动失败同样报错退出。

## 接入本机 AI CLI

CLI 后端都使用运行 Agent 的同一系统账户的登录状态，凭据留在本机；YAML 不填 `base_url`、API Key 或认证文件，ID 只能在本地声明，Gateway 下发的配置不能新增 CLI ID。设置了 `gateway.allowed_runtimes` 时，把新 ID 一并加入并重启 Agent。

### Claude Code

先以运行 Agent 的账户执行 `claude auth login`，确认 `claude` 在 Agent 的 PATH 中，然后声明：

```yaml
runtimes:
  - id: claude-local
    kind: claude
```

也可以在设置页面「本地 AI」分组选择 Claude Code CLI。Gateway 必须配置控制面 API Key 校验和共享 `-redis-addr`；静态 key 没有租户归属，不能用于原生 Claude 续接。

- 模型目录由 CLI 的 `initialize` 控制请求获取，不执行推理；只发布 `resolvedModel` 返回的真实 ID，不发布 `default`、`sonnet`、`opus` 等简称，请求的模型 ID 原样传给 `--model`。启用别名路由时，公共别名须指向目录中实际存在的目标。
- 每个运行时默认 8 个 CLI 会话，`max_concurrent` 最多 32；名额满时淘汰等待工具结果最久的空闲会话。CLI 生成阶段沉默两分钟才回收，持续输出的长回合不受限。
- 客户端需要关闭缓存和实验 beta；原版 Claude Code 客户端建议关闭 thinking。
- 请求参数、续接规则与联调命令见 [Claude 包说明](claudecode/README.md)。

### Codex

默认不需要手写声明：用运行 Agent 的账户执行 `codex login` 后，`-auto-discover`（默认开）在两分钟内自动注册 `codex-local`。要显式管理或使用多个账户时手写声明（已有任何 `kind: codex` 运行时就不再自动注册）：

```yaml
runtimes:
  - id: codex-local
    kind: codex
```

先确认 `codex login status` 为 ChatGPT 登录、Agent 的 PATH 能找到 `codex`。改完 YAML 后发送 `SIGHUP` 或在设置页面保存即重新加载（白名单改动仍需重启）：

```bash
go run ./service/aiServeWeaveAgent -config service/aiServeWeaveAgent/config.yaml
```

请求能力、上限、图像生成与客户端配置见 [Codex 包说明](codexcli/README.md)。

### 同时接入多个 CLI

Claude Code 与 Codex 可以由同一个 Agent 提供，各自有独立的运行时 ID、登录状态、健康检查和模型目录：

```yaml
gateway:
  allowed_runtimes:          # 配置白名单时必须同时包含两个 ID
    - claude-code-local
    - codex-local
runtimes:
  - id: claude-code-local
    kind: claude
  - id: codex-local
    kind: codex
```

Gateway `/v1/models` 合并所有就绪运行时的模型；一个 CLI 未就绪只隐藏它自己的模型。若 Codex 没有模型，先在启动 Agent 的同一终端执行 `codex --version` 确认可执行依赖完整，再检查登录和运行时状态；重新构建 Gateway 不会修复损坏的本机 CLI 安装。

其他编程智能体 CLI（`grok`/`opencode`/`cursor`/`codebuddy`/`qoder`/`pi`）见 [CLI 包说明](cliagent/README.md)。

## 托管供应商 Key

托管供应商（`kind: openai`、`anthropic`、`openai_compatible` 等）的 `base_url` 留空使用官方端点（两种 compatible 必填），`models` 可声明模型目录（空格或逗号分隔）。Key 有三种来源：

- **设置页面填写**：保存为配置目录下 `keys/<id>.key`（文件 0600，目录 0700），YAML 只写 `api_key_file` 路径；页面只显示「已保存」、从不回显。留空保持不变，可勾选清除；改动 `base_url`、种类或 ID 后已存 Key 不再跟随，需要重新填写；不再被引用的受管 Key 文件在保存后删除。
- **手写 `api_key_file`**：指向其他私有文件，页面清除时只解除引用、不删除文件。
- **`api_key_env`**：只在文件中保存环境变量名。

两者都设置时 `api_key_file` 优先；文件读不到时 Key 为空，不回退到环境变量。`-check-config` 报告读不到或权限过宽的 Key 文件。

OpenAI 兼容种类（含 vllm/sglang/ollama 等本地服务）可写 `reasoning_parser: think_tags` 或 `think_tags_prefilled`，把内联的 `<think>…</think>` 切成思考与回答（见 [runtime README](../../common/runtime/README.md#思考开关与兜底解析器)）；设置页面不展示该字段，但保存时按 id 与 kind 保留。端点与 Key 规则见 [托管供应商说明](../../common/runtime/hosted/README.md)。

### 探测期限

每个运行时可写 `probe_timeout`（Go duration，如 `45s`），作为单次身份探测、健康检查和模型目录读取的期限。留空使用种类默认值：本地 CLI 60 秒，托管供应商 10 秒，其他 3 秒。取值须大于 0 且不超过 `10m`；裸数字（如 `45`）、非正值或超过上限的值会让启动、`-check-config` 与热重载都报错并指出运行时 ID。设置页面不展示该字段，但保存时按 id 与 kind 保留；修改后热重载即生效。由 Gateway 下发配置创建的实例使用下发配置里的期限（未设置时取种类默认值），不读本地的 `probe_timeout`。

```yaml
runtimes:
  - {id: codex-local, kind: codex, probe_timeout: 2m}
```

本地 CLI 启动较慢时（CLI 本地状态很大，例如 Codex 的 `sessions` 目录积累到 GB 级；机器负载高；网络需走代理），探测会超时，运行时保持未就绪并定期重试。原因会写进 Agent 日志：首次失败、原因变化时各记一条 `runtime not ready; retrying`，原因不变时每 10 分钟重复一次，超时还带 `hint` 字段；已就绪的运行时变为不健康或恢复时也各记一条。自动发现的 `codex-local` 使用默认期限；要调整，就在 `runtimes:` 里显式声明 `kind: codex`。

## 本地 CLI 账户额度

正常运行的 Agent 在本地配置页面（`-config-ui-addr`）的「推理后端」中显示「CLI 账户额度」。只查询本地声明的 `kind: codex` / `kind: claude`（配置文件声明的，以及 Agent 自动发现并注册的 `codex-local`；后者不会因热重载而被移出），每个运行时一行（名称后附运行时 ID）；声明了不同 `account_dir` 的运行时各自查询自己的账户（见[同一 CLI 的多个账户](#同一-cli-的多个账户)）。仅配置模式尚未启动运行时，不执行账户查询。

- Codex 使用原生 app-server 的 `account/rateLimits/read`，读取计量桶的主/次窗口、已用比例、窗口时长与重置时间；多桶响应优先于兼容单桶响应。上游未报告重置时间的窗口无法判断何时过期，不显示，其余窗口照常展示。
- Claude Code 使用原生 stream-json 控制请求 `get_usage`（`skip_behaviors: true`），读取 5 小时、7 天及可用的 Sonnet/Opus 周窗口。该接口是 CLI 的实验能力，旧版本或账户权限可能不提供；可缺失窗口不显示，所有窗口都缺失时显示暂不可用，不推算额度。
- 剩余比例为 `max(0, 100 - 已用比例)`，不是可用 token 数或余额金额；平台租户配额、每次推理 token 与 CLI 共享账户的订阅窗口相互独立。
- 页面渲染只读缓存，异步查询每分钟最多一批、各运行时并发执行，共享 15 秒超时。查询不随发起它的页面请求取消，刷新或关闭页面不会把本轮记为失败。点击刷新也遵守一分钟缓存。查询时间和重置时间以 UTC 显示；超过两分钟标记旧数据，窗口到重置时刻后隐藏比例，等待重新查询。
- 未登录、未提供额度、查询失败分别显示提示。失败会清除上一轮额度，避免把旧结果当作当前余量。查询不提交提示词、不执行模型推理，不读取或导出 token、钥匙串秘密；仍由 CLI 自行管理登录，原始输出和错误不进入页面或日志。

Codex 与 Claude Code 运行时在模型发现时另执行一次 `codex --version` / `claude --version`（5 秒超时，只取其中的版本号），作为运行时版本上报，控制台「版本」列据此显示；CLI 未输出版本号时仍显示未上报。

Agent 必须与 `codex login` / `claude auth login` 使用相同系统账户和账户目录（声明了 `account_dir` 时即该目录）；页面只监听回环地址。隧道上报的额度带运行时 ID，控制面租户模型页按运行时 ID 精确匹配；来自旧版 Agent、不带运行时 ID 的条目按 kind 匹配。离线验证：

```bash
go test ./service/aiServeWeaveAgent/cliusage ./service/aiServeWeaveAgent/codexcli ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveAgent/configui
```

上游参考：[Codex app-server 账户额度](https://learn.chatgpt.com/docs/app-server#6-rate-limits-chatgpt)、[Claude Code 订阅窗口说明](https://code.claude.com/docs/en/statusline#rate-limit-usage)。真实账户查询与离线解析测试应分别验收，离线通过不代表本机已登录或已获取额度。

## 同一 CLI 的多个账户

`kind: codex` / `kind: claude` 的运行时可以声明 `account_dir`，让同一台机器以多个订阅账户对外服务。Agent 启动该运行时的 CLI 进程时，把它设为 `CODEX_HOME`（Codex）或 `CLAUDE_CONFIG_DIR`（Claude Code），登录、会话与额度都随目录隔离：

```yaml
runtimes:
  - {id: codex-a, kind: codex}                               # 系统默认登录
  - {id: codex-b, kind: codex, account_dir: ~/.codex-b}      # 第二个账户
  - {id: claude-b, kind: claude, account_dir: ~/.claude-b}
```

- 每个目录先用对应 CLI 登录一次：`CODEX_HOME=~/.codex-b codex login`、`CLAUDE_CONFIG_DIR=~/.claude-b claude auth login`。Agent 不代为登录，也不读取目录里的凭据。
- 路径必须是绝对路径；`~/` 开头会按 Agent 进程的家目录展开。只有 codex 与 claude 两种接受该字段，其他种类声明它会在启动或重新加载时被拒绝。
- `account_dir` 只在本地生效：Gateway 下发的配置既不能设置也不能改动它，注册表总是取本地声明里的值。
- 本地设置页面的推理后端条目中，codex / claude 多出「账户目录」输入框，留空即使用系统默认登录。
- 与其他运行时一样，新 ID 需要加入运行时白名单（显式配置了白名单时）才会被 Gateway 调度。

Claude Code 与 Codex 账号都可经平台[独享包月售卖](../../docs/billing-accounts.md)，以 Agent ID + 运行时 ID 作为商品身份。同一个 Agent 中的两种账号可以分别售给不同商户，每个商品只授权对应的账号，不独占整个 Agent。每个实际登录只通过该商品对应的运行时提供服务；不要把同一登录从其他运行时或 Agent 继续共享。已售出后保持运行时 ID、账户目录及实际登录稳定。

## 运行时热重载

修改 `-config` 文件的 `runtimes:` 列表后，向 Agent 发送 `SIGHUP`（`kill -HUP <pid>`），或在本地设置页面点「保存」，Agent 会不重启地调和运行时：新增的加入、改动的替换、删掉的移除，CLI 额度监视器同步更新。页面保存后会提示重新加载成功或失败（详情见 Agent 日志）。

- 文件无法读取、存在重复 ID 或任一运行时配置非法时，本次重新加载整体放弃，运行中的运行时不受影响；越过这一关后各运行时独立调和，个别失败只保留该运行时原状。
- 只调和文件声明的运行时；`-ollama-url` 声明的、自动发现的与 Gateway 下发的实例不受影响。
- 自动发现：`-auto-discover`（默认开）下，Agent 还会每两分钟检查本机是否装了并登录了 Codex CLI，是则以 `codex-local` 注册；PATH 里没有 `codex`、未登录、已有 `kind: codex` 声明、或 `allowed_runtimes` 非空且不含 `codex-local` 时都不注册。`-auto-discover=false` 关闭。Ubuntu 服务的 PATH 不含 nvm、npm 全局目录等，安装时用 `--path` 补上。
- Gateway 端点、证书、运行时白名单、指标与日志等其余设置仍需重启。重新加载出白名单外的运行时 ID 时，日志会给出警告。
- 没有 `-config` 文件（也没有设置页面的配置路径）时，`SIGHUP` 只记录一条日志，不做任何改动。

## 额度耗尽与限流

本地 CLI 账户的订阅额度用完时，Agent 把失败报告为可重试的 `rate_limited`，并在隧道错误的 `retry_after` 中带上额度恢复所需的时长，Gateway 据此把这个运行时搁置到恢复为止，同一模型的请求转去其他账户或节点：

- Codex：回合以 `usageLimitExceeded` / `rateLimitExceeded` 结束（`turn/completed` 或 `willRetry=false` 的 `error` 通知）时，Agent 趁 app-server 尚未退出，再以 `account/rateLimits/read` 读取窗口，取所有已用满（≥100%）窗口中最晚的重置时刻。`willRetry=true` 的 `error` 通知由 Codex 自行重试，不视为失败。
- Claude Code：stream-json 中 `rate_limit_event` 的 `rate_limit_info.status` 为 `rejected` 时记下 `resetsAt`，随后的错误结果或缺失结果即判为额度耗尽。
- OpenAI 兼容的 HTTP 后端返回 429 时，`Retry-After` 的秒数形式同样透传（日期形式与超过 24 小时的值视为未知）。
- 重置时间未知时 `retry_after` 留空，由 Gateway 使用默认冷却时长。上述事件格式按 CLI 当前版本实现，尚未在真实账户耗尽场景下验收。

## 整机准入与负载上报

Agent 每 `-admission-sample-interval`（默认 5s）采集一次本机实时 CPU 与内存，经每次心跳上报 Gateway（`Heartbeat.load`）。采样与上报始终开启：Gateway 用它把资源紧张的节点排到最后，Console 机群页的「实时负载」也显示它。在此之上，可以选择开启两道本机防线，避免 CLI 或其他任务把机器压垮：

1. **资源闸门**：
   - CPU 连续 2 次采样 ≥ `-admission-cpu-high`%，或可用内存 ≤ `-admission-memory-low`%（1 次即触发）时，节点进入「资源紧张」；
   - 处于资源紧张时，新的重任务立即以可重试的 `backpressure` 拒绝，Gateway 换节点或进入它的等待队列；
   - 退出需要连续 2 次回到阈值以外 `-admission-hysteresis` 个百分点，并且至少保持 15 秒；
   - 采样连续失败 3 次会解除资源紧张状态，避免坏掉的探测让节点永远拒绝工作。
2. **本机信号量**：
   - `-admission-cli-max` 限制所有本地 CLI 运行时（claude、codex、gemini-cli、cliagent 系列）的同时请求数，`-admission-general-max` 限制其他本地后端（ollama、vllm 等）的同时请求数；
   - 满了以后最多等待 `-admission-queue-max-wait`（默认 5s），每类最多 `-admission-queue-max-waiters`（默认 16）个请求等待，超时或等待数满时以 `backpressure` 拒绝；
   - 这两个上限叠加在每个运行时自己的 `max_concurrent` 之上。

所有阈值与上限默认为 0（关闭），升级后在设置之前行为不变。

| 操作 | 资源闸门 | 信号量 |
| --- | --- | --- |
| Chat、流式 Chat、Messages、图像生成、Embed、Rerank、转录 | 是 | 本地 CLI 占 `cli`，其他本地后端占 `general` |
| 工作流提交、输入上传 | 是 | 否（提交只持续几毫秒，GPU 工作在 ComfyUI 自己的队列里） |
| 模型列表、工作流状态/事件/取消、产物读取 | 否 | 否（满载时仍能回应已接下的工作） |
| 托管供应商（OpenAI、Anthropic 等） | 否 | 否（算力在远端） |

健康/inspect 探测、CLI 额度轮询和 Claude 会话在两轮之间的驻留不受准入控制，会话数仍由 Claude 运行时自己的上限管。YAML 写法如下，命令行显式传入的 flag 优先，修改后需要重启：

```yaml
admission:
  cpu_high_percent: 90
  memory_low_percent: 10
  cli_max: 6
  general_max: 16
  queue_max_wait: 5s      # "0s" 表示立即拒绝
  queue_max_waiters: 16
```

平台差异：

- Linux 读 `/proc`；在 cgroup v2 中运行（容器）时，内存按 cgroup 限额计算，CPU 仍按整机口径。
- macOS 不使用 cgo，CPU 由 1 分钟负载均值除以核数估算，并在上报中标记为估算值。
- Windows 经 kernel32 读取，没有负载均值。

本机 `/metrics` 上的相关指标：

- `agent_admission_pressure`
- `agent_admission_rejections_total{class,reason}`
- `agent_admission_wait_seconds{class,outcome}`
- `agent_admission_inflight/waiting{class}`
- `agent_host_cpu_utilization`
- `agent_host_memory_available_bytes`
- `agent_host_memory_total_bytes`
- `agent_host_load_average_1m`
- `agent_host_sample_errors_total`

## Gemini CLI 图片生成

在本地声明 `kind: gemini-cli`，通过 Gemini CLI 官方 Nano Banana 扩展提供 `gemini-image` 图片模型，复用 Gateway 的 `/v1/images/generations` 与既有图像流隧道。该后端仅支持单张文生图，直接调用扩展的固定 MCP 工具；凭据只从 Agent 本机环境读取。现有 `kind: gemini` 的托管聊天用途不变。

安装、YAML、API 示例、隔离规则与实机验收边界见 [geminicli/README.md](geminicli/README.md)。

## 预编译独立部署

Ubuntu systemd、Apple Silicon macOS LaunchAgent、Windows 11 原生用户计划任务的安装与维护见 [Agent 部署指南](../../deploy/agent/README.md)。包含固定版本在线/本地安装、状态/停止/重载、显式升级/回退与保留身份卸载。目标系统实机验收状态另列，不能由构建通过推定。

本机开发时，可以在本目录用 `make install` 把当前源码构建为 `com.aiserveweave.agent.dev` LaunchAgent（仅 macOS），默认读取本目录的 `config.yaml`，工作目录也是本目录。这个服务与正式服务的 Label、程序目录和日志目录都不同，互不覆盖。改代码后重新运行 `make install` 即可。`make status`、`make logs`、`make reload`、`make stop`、`make uninstall` 用于维护，`make help` 列出全部目标；用 `CONFIG=...` 指定其他配置。
### 本机开发命令

在本目录执行。`CONFIG` 可写相对路径，Makefile 会转成绝对路径写入 LaunchAgent。配置里的 `ca_file`、`cert_file`、`key_file`、`bootstrap_token_file` 不做这种转换，请写本机实际存在的绝对路径；文件不存在时 Agent 启动即退出。

```bash
# 只校验配置，不联网
make check-config CONFIG=../../deploy/config.open7x.yaml

# 常驻运行：构建 → 生成 plist → 重新加载 LaunchAgent。改代码或改 CONFIG 后重跑
make install CONFIG=../../deploy/config.open7x.yaml

# 前台调试：日志直接输出到终端，同时保存一份，Ctrl-C 退出
make run CONFIG=../../deploy/config.open7x.yaml 2>&1 | tee /tmp/aisw-agent-fixed.log
```

- `make install` 和 `make run` 不要同时运行：两个进程使用同一个节点身份，还会争抢 `metrics_addr` 和设置页端口。前台调试前先 `make stop`，调试完再 `make start`（或重跑 `make install`）。
- 首次安装时输出 `make: [install] Error 3 (ignored)` 是正常的：这一步是卸载旧服务，服务还没加载时会报这个错，Makefile 忽略它。
- `已启动 com.aiserveweave.agent.dev` 只说明 launchd 已加载服务，不代表 Agent 启动成功。Agent 异常退出后，launchd 每 15 秒重新拉起一次，这时 `state = running` 也可能是在循环崩溃。用 `make status` 看 `runs` 和 `last exit code`：`runs` 不增长、`last exit code = (never exited)` 才算稳定运行。再用 `make logs` 确认日志里有 `agent started`；如果连了 Gateway，还应有 `tunnel connected`，且没有接着出现 `agent exited with error`。
- `make status`、`make reload` 读取的是已安装服务实际使用的配置（plist 里的 `-config`），不需要再传 `CONFIG`。
- `make status` 的“资源”一节列出 Agent 主进程及其全部子孙进程（claude/codex 等 CLI）的 `%CPU`、RSS 和运行时长，并给出合计。`%CPU` 是 `ps` 的近期均值、按单核计，多核满载时可超过 100%。
- 后台服务不读 shell 启动文件，`claude`、`codex` 等 CLI 只能通过 `make install` 时的 `PATH` 找到。如果执行 `make install` 的终端 PATH 不全，可以用 `AGENT_PATH=...` 指定。
- 日志位置：`~/Library/Logs/AIServeWeave/AgentDev/agent.log`（Agent 日志）和 `stderr.log`（进程崩溃输出）。