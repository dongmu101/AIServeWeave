# Gemini CLI 图片生成

本地 `kind: gemini-cli` 复用 Gemini CLI 官方 [Nano Banana 扩展](https://github.com/gemini-cli-extensions/nanobanana)，仅提供图片生成。它直接通过 stdio MCP 调用扩展中的 `generate_image`，不启动 Gemini CLI 的聊天模型，不加载 CLI 的设置、记忆、hooks 或其他扩展，也不让提示词决定要执行哪些工具。

`kind: gemini` 仍是原有的托管 Gemini API 聊天后端，两者独立。

## 安装和配置

在运行 Agent 的系统账户下安装扩展；需要 Node.js 20+、npm 和 Gemini CLI：

```bash
gemini extensions install https://github.com/gemini-cli-extensions/nanobanana
export NANOBANANA_API_KEY="your-gemini-api-key"
```

若扩展没有构建出 `mcp-server/dist/index.js`，按扩展自己的安装说明完成构建。Agent 不会自动下载、安装或构建外部代码。

运行 Agent 的进程必须继承这个环境变量。仅在交互式终端设置变量，不会让 systemd、launchd 或容器里的既有进程自动拿到它。CLI 的 Google 登录态及订阅额度不作为图片 API 凭据使用；图片调用遵循 Gemini API 的权限、配额和计费。

在 Agent YAML 的 `runtimes` 中添加：

```yaml
runtimes:
  - id: gemini-local
    kind: gemini-cli
    max_concurrent: 2
    request_timeout: 5m
```

`base_url`、`api_key_env`、Headers 与 TLS 留空。设置了 `gateway.allowed_runtimes` 时，把 `gemini-local` 加进去。重启 Agent，或发送 `SIGHUP` 只重新加载运行时（白名单改动仍需重启，见 [Agent README「运行时热重载」](../README.md#运行时热重载)）。也可在本地设置页的「本机 CLI」里选择「Gemini CLI（Nano Banana 生图）」。

扩展从 `${GEMINI_CLI_HOME}/.gemini/extensions/nanobanana` 读取；`GEMINI_CLI_HOME` 未设置时使用 `$HOME`。只接受官方 manifest 的 `node ${extensionPath}/mcp-server/dist/index.js` 布局，不执行 manifest 中自定义的命令或环境变量。

凭据按以下优先级读取，选中后仅作为子进程的 `NANOBANANA_API_KEY` 传入：

1. `NANOBANANA_API_KEY`
2. `NANOBANANA_GEMINI_API_KEY`
3. `NANOBANANA_GOOGLE_API_KEY`
4. `GEMINI_API_KEY`
5. `GOOGLE_API_KEY`

推荐使用第一项，避免与其他后端共用凭据。Agent 不读取扩展的 `.env`、CLI Keychain 或 OAuth 文件。

`NANOBANANA_MODEL` 可在 Agent 本机选择上游图片模型；不设置时使用扩展的默认模型。对外始终只发布 `gemini-image`，调用方不能覆盖上游版本。

## 调用

扩展就绪后，Gateway 的 `/v1/models` 会出现仅带 `image_generation` 能力的 `gemini-image`。通过现有图像端点调用：

```bash
curl https://your-gateway/v1/images/generations \
  -H "Authorization: Bearer $AISW_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-image","prompt":"一只在太空喝咖啡的橘猫","n":1,"response_format":"b64_json"}'
```

复用 Gateway 推理生图路径的校验与超时；若生成时间较长，需相应配置 Gateway 的 `-images-generation-timeout`。该路径只返回 `b64_json`；不支持图片编辑、批量生成、尺寸/质量/风格控制、图片输入和聊天。这些限制由已有前门按字段拒绝。

## 隔离与生命周期

- 必须在 Agent 本地声明运行时，Gateway 不能创建或改换种类。
- 每次探测或生图使用独立临时目录，子进程的 `HOME`、`TMPDIR` 都指向该目录。子进程只接收选中的图片 Key、本机模型选项、PATH、语言、代理和证书变量；不传 `NODE_OPTIONS`、其他服务的 API Key 或 CLI 设置目录。
- MCP 先完成 `initialize`、`notifications/initialized`、`tools/list`；生图时只调用一次 `generate_image`，固定 `outputCount: 1`、`preview: false`、`noPreview: true`、`format: separate`。远程输入只进入 `prompt`，不进入 argv、文件路径或命令。
- 扩展原有 stderr 会输出提示词，所以全部丢弃。MCP 返回的路径与原始错误同样不记录、不对外转发。
- 回收进程组后，只读取临时目录中 `nanobanana-output` 下唯一的普通文件。拒绝目录/文件符号链接、额外文件、空文件、超过 32 MiB 的文件与无法按字节识别为 PNG/JPEG/WebP/GIF 的文件；从不打开 MCP 文本报回的路径。
- 文件在临时目录删除前有界读入内存，然后经既有 `ImageMeta` + 最大 256 KiB 字节分块回传。单条 MCP 消息最多 1 MiB，一次进程的协议输出最多 4 MiB。
- 并发受 `max_concurrent` 限制，总期限由注入的 `runtime.Clock` 控制；生图工具可能长时间静默，因此不使用流空闲期限。调用方取消、超时或 Agent 关闭都会终止并回收整个进程组。
- 仅支持 macOS/Linux。扩展本身是运维在本机安装的受信任代码；临时目录与允许列表不构成操作系统沙箱。

## 验证边界

Probe/Health/Discover 只验证 Node、安装布局、Key 是否存在及 MCP 生图工具目录，不执行图片推理，也不验证 Key 是否有效或是否有余额。缺 Key、缺扩展或协议不兼容时不发布模型。

默认测试使用独立子进程模拟官方 MCP 协议，不依赖 Node、外部网络或付费账户。真实图片生成需要单独用有效 Key 验收；本次未执行付费生图。

2026-10-19 另用临时安装的官方扩展 1.0.12（提交 `5badc5aafea8751fe059a054b629cf2d989bceb1`）验证了真实 `initialize` / `tools/list` 握手、Probe 与 `gemini-image` 模型发现。该检查使用占位 Key、不调用 `tools/call`，所以不证明 API Key、上游权限或实际生成可用；没有改动本机既有的 Gemini CLI 配置。

官方协议依据：[扩展 manifest](https://github.com/gemini-cli-extensions/nanobanana/blob/main/gemini-extension.json)、[MCP 工具定义](https://github.com/gemini-cli-extensions/nanobanana/blob/main/mcp-server/src/index.ts)、[输出文件处理](https://github.com/gemini-cli-extensions/nanobanana/blob/main/mcp-server/src/fileHandler.ts)。
