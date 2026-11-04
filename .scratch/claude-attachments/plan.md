# Claude CLI 文件与视频附件方案

状态：用户已批准并已实现本地代码；默认/race 与真实 CLI PDF 传输契约已通过，真实转换器及线上验收待目标 Agent。语音后端为 Agent 本地 whisper.cpp。2026-11-04。

## 已确认范围

- 延续 Claude Code → Gateway `/v1/messages` → Agent `kind: claude` 的调用链。
- 文件：PDF、文本、源代码、CSV、JSON、Markdown，以及 Word、Excel、PowerPoint；Office 同时考虑现代格式和旧格式。
- 视频：抽取画面并识别语音，输出带时间信息的图片与转录文本。属于视频采样分析，不声明逐帧理解。
- 附件必须参与完整历史校验，覆盖首轮、普通多轮、工具结果、孤立工具回合恢复；不能只放开类型校验后丢弃内容。
- 已交付的图片链路保持可用。不改变 CLI 的订阅登录、工具执行归属或凭据边界。

## 当前证据与边界

- 原生 Messages 隧道传递 JSON，整个请求上限 32 MiB；含 base64 与历史附件，不能据单个文件大小判断一定可接受。
- 本机 Claude CLI 2.1.318 已经通过模拟 HTTP 上游验证：结构化 user 输入中的 PDF `document` 块能到达上游。该实验没有验证实际 PDF 理解。
- 项目有音频转录接口及出站 HTTP 编码，但没有已验证可直接复用的语音模型或本地视频解码器。
- 当前机器没有 `ffmpeg`、`ffprobe`、`whisper-cli`、LibreOffice；转换依赖的实机验收需要准备工具环境。
- 目前代码不含原生视频内容类型；视频不能直接冒充 Claude 的原生输入能力。

## 接入契约

- Messages 的 PDF、文本与 Office 文件使用 `document` 块；`source.type` 支持 `text`（文本）与 `base64`（文件字节），保留 `title`。Office MIME 是本平台对文档输入的扩展，先转换，再传给 CLI。
- 视频使用平台扩展 `video` 块，`source` 为内联 base64，MIME 第一版支持 MP4、MOV、WebM，保留可选标题。文档必须给出完整请求示例，并明确它不是 Anthropic 原生视频协议。
- 不在本次隐含新增 `/v1/files`、跨租户文件 ID 存储、URL 下载或客户端本地路径访问。Claude Code 客户端是否能选择并发送视频，还需单独验证；网关接受视频不等于所有客户端都能上传它。
- OpenAI Chat/Responses 输入附件适配作为同一方案中的待确认扩展项；默认先完成最初报错的 Messages 路径，不声称其他前门已支持。

## 转换路径

| 类型 | 路径 | 内容边界 |
| --- | --- | --- |
| UTF-8 文本、代码、CSV、JSON、Markdown | 解码为带文件名的文本块 | 不是可执行指令，不执行源代码或公式 |
| PDF | 首轮/历史以原生 document 块传入 CLI | 工具结果使用本地 Poppler 转为文字与带页码图片，不依赖 MCP PDF 资源行为 |
| DOC/DOCX、XLS/XLSX、PPT/PPTX | 独立 LibreOffice headless 进程转 PDF，再走文档链路 | 保留可渲染的版式、表格与图表；批注、隐藏内容、演讲备注不默认等同于导出页面 |
| MP4、MOV、WebM | FFprobe 探测，FFmpeg 均匀采样画面与提取音轨，再做语音转录 | 图片带时间戳；音轨存在且转录失败时明确失败，不能冒充已处理声音；无音轨正常处理并标记 |

依赖通过 Agent 本地配置指定可执行文件与模型路径，不接受请求或 Gateway 下发命令、参数模板或模型下载地址。不新增云服务凭据作为隐式前提。语音后端已确定为 Agent 本地 whisper.cpp，转为单声道 16 kHz WAV 后识别，保留分段时间戳，不向外部语音服务上传音轨。

建议的本地配置形状（新字段，尚未实现；示例路径不是当前主机的已安装路径）：

```yaml
attachments:
  libreoffice_path: /usr/bin/libreoffice
  pdfinfo_path: /usr/bin/pdfinfo
  pdftoppm_path: /usr/bin/pdftoppm
  pdftotext_path: /usr/bin/pdftotext
  ffmpeg_path: /usr/bin/ffmpeg
  ffprobe_path: /usr/bin/ffprobe
  whisper_path: /opt/whisper.cpp/build/bin/whisper-cli
  whisper_model_path: /opt/whisper.cpp/models/ggml-small.bin
  max_file_bytes: 20971520
  max_document_pages: 30
  max_video_seconds: 300
  max_video_frames: 24
  max_concurrent: 1
  timeout: 180s
```

缺失配置的转换器保持该类输入不可用；不自动安装程序、下载语音模型或改变系统 PATH。已有文本/图片及无需转换程序的 PDF/文本文件不依赖这些程序。

## 运行边界与初始默认值（已确认）

- 单附件解码后最多 20 MiB，同时受整份请求 32 MiB 上限约束；超限明确报错，不静默丢页或截断文件。
- 转换文档最多 30 页；视频最多 5 分钟、最多 24 张时间戳画面，最长边缩放至 1280。
- 转换任务每个 Agent 默认并发 1、队列有界；单任务 180 秒超时，支持调用方取消及完整子进程回收。
- 使用每请求独立临时目录；完成、失败、取消均清理。限制生成文件总量与大小，不把原件路径或内容放进日志。
- Office 使用独立用户配置，关闭宏、恢复与外部链接更新；FFmpeg 只访问已写入的本地输入和必要的本地解码协议。
- 配置缺失不影响已有文本/图片推理；请求需要的转换依赖不可用时返回可诊断错误，不假报成功。

## 实现边界

1. Agent 内新增附件转换模块，返回 CLI 可消费的文本、图片、PDF；进程管理复用 `internal/cliprocess`。
2. Claude 请求类型、严格校验、结构化 CLI 输入、MCP 工具结果转换与历史校验接入同一模块；比对原始附件历史，不用有损转换结果鉴权。
3. 转换结果保留在所属 CLI 会话内，避免工具续接反复转码；新会话依据完整原始历史重新构建；不跨租户共享附件缓存。
4. Agent 本地 YAML 增加 `attachments` 配置（工具路径、语音模型/服务配置、资源限额）；设置页保存其他配置时必须保留它。可执行文件和语音凭据不进入隧道配置。
5. 同步 Agent/Gateway STATUS、Claude README、配置示例、依赖边界与部署验收矩阵。

## 验收与顺序

1. 固定附件协议、语音后端、默认限额和首期前门范围后实现。
2. 先补 PDF/文本首轮、历史与工具结果；用真实 CLI 加模拟上游校验内容形状。
3. 补 Office 转换与视频画面/音轨处理，失败路径覆盖缺依赖、超限、无音轨、转录失败、取消与清理。
4. 用模拟转换进程验证默认测试；再用真实 Office、PDF 与短视频样本验收转换内容，不能把假进程测试当作真实转换验收。
5. 跨 Gateway、真实 mTLS 隧道验证 JSON/SSE、工具循环及历史改动拒绝；运行 race 检查及 Windows/Linux/macOS 构建检查。
6. 在有 Claude 登录和转换依赖的实际 Agent 上验证 PDF、Office、视频问答；部署后再验证用户入口。未完成前明确标记未实机验收。

实施记录：工具 PDF 采用 Poppler 页图加文本路径，避免依赖未经确认的 MCP PDF 资源行为；Office 原生 PDF 输出增加本地 `pdfinfo_path` 校验页数。配置、协议、错误和验收实况集中记录在 `service/aiServeWeaveAgent/attachments/README.md`。当前环境没有真实转换器/语音模型，不将模拟进程测试记为真实转换验收。

## 参考

- [Claude PDF 输入](https://platform.claude.com/docs/en/build-with-claude/pdf-support)
- [Claude 文件输入与其他格式转换](https://platform.claude.com/docs/en/build-with-claude/files)
- [whisper.cpp CLI](https://github.com/ggml-org/whisper.cpp/tree/master/examples/cli)
- [CLIProxyAPI 图片与文档转换](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/internal/translator/claude/openai/chat-completions/claude_openai_request.go)
