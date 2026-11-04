# Claude Messages 文件和视频附件

本模块只接入 `kind: claude` 的 `/v1/messages` 原生路径，覆盖首轮、完整历史、工具结果及孤立回合恢复；OpenAI Chat/Responses 和其他运行时没有随之开放文件或视频输入。

## 请求格式

文件使用 `document` 块，视频使用本平台扩展 `video`；后者不是 Anthropic 原生协议，也不代表原版 Claude Code 客户端有视频上传入口。下列内容块可放入 user 的 `content` 或 `tool_result.content`。

```json
{"type":"document","title":"report.pdf","source":{"type":"base64","media_type":"application/pdf","data":"<PDF base64>"}}
```

```json
{"type":"document","title":"notes.md","source":{"type":"text","media_type":"text/plain","data":"文件内容"}}
```

```json
{"type":"video","title":"demo.mp4","source":{"type":"base64","media_type":"video/mp4","data":"<video base64>"}}
```

例如 Messages 请求为 `{"model":"claude-opus-5-5","max_tokens":2048,"messages":[{"role":"user","content":[{"type":"text","text":"总结附件"}, <上述内容块>]}]}`，将占位内容替换成完整块后再作为 JSON 发送。独立音频、URL、客户端文件路径、`file_id`、`/v1/files` 不在本次范围。

| 文件 | MIME / 编码 |
| --- | --- |
| 文本、代码、CSV、Markdown | UTF-8，`text/*`，或上面的 `source.type: text` |
| JSON、XML、JavaScript | `application/json`、`application/xml`、`application/javascript`，UTF-8 |
| PDF | `application/pdf`，base64 |
| DOC / DOCX | `application/msword` / `application/vnd.openxmlformats-officedocument.wordprocessingml.document` |
| XLS / XLSX | `application/vnd.ms-excel` / `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` |
| PPT / PPTX | `application/vnd.ms-powerpoint` / `application/vnd.openxmlformats-officedocument.presentationml.presentation` |
| MP4 / MOV / WebM | `video/mp4` / `video/quicktime` / `video/webm`，base64 |

Office MIME 是本平台文档扩展，先通过 LibreOffice 转 PDF，再传给 CLI；保留可渲染的版式、表格和图表，不保证隐藏表、批注或演讲者备注均出现在导出页面。工具返回的 PDF 转成带页码的图片及提取文本，避免把 PDF base64 当作文本交给模型。

视频约每 5 秒采样一帧，达到帧数上限时按全时长均匀采样；图片最长边 1280，文本标明总时长、采样数量和近似时间点。这是采样分析，不能保证发现帧间短暂事件。有音轨时转为单声道 16 kHz PCM WAV，交给本地 whisper.cpp 输出带时间戳的 SRT 文本；不向外部语音服务上传音轨。无音轨明确标记；空转录表示未识别到语音；识别失败或缺依赖时整个附件明确失败。

## Agent 本地配置

以下为 Unix 示例路径，必须改成真实路径；Windows 使用实际绝对 `.exe` 与模型路径，可含空格。修改后重启 Agent。设置页面保存其他配置时保留此段，目前没有专用转换器表单。

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

| 场景 | 本地依赖 |
| --- | --- |
| 文本、首轮/历史中的原生 PDF | 无 |
| 工具结果中的 PDF | Poppler 的 `pdftoppm`、`pdftotext` |
| Office | LibreOffice；首轮/历史需 `pdfinfo` 校验页数；工具结果需 Poppler 渲染及提取文本 |
| 无音轨视频 | FFprobe、FFmpeg |
| 有音轨视频 | FFprobe、FFmpeg、whisper.cpp 的 `whisper-cli` 和本地多语言模型 |

程序和模型只从本地配置读取，不接受请求或 Gateway 下发的命令、参数模板、模型地址。不自动安装程序、下载模型或改变 PATH。依赖必须准备在实际承载 Claude 的 Agent 上，安装在 Gateway 主机无效。各平台入口和版本需实机验收，尤其 LibreOffice 发行版启动器及 Windows `.exe`。

## 限额与生命周期

- 默认单附件解码后最多 20 MiB；原始 Messages（含 base64 和历史）及转换后的 CLI 输入分别受 32 MiB 上限约束。单附件满足限制不等于整个请求一定通过。
- Office 转 PDF、工具 PDF 渲染最多 30 页；直接传 CLI 的原生 PDF 不做本地页数解析，其格式、页数和内容限制由 CLI/上游判断。不静默丢页或截断文件。
- 视频最多 300 秒、24 帧，单次转换超时 180 秒。可配置上限：文件 32 MiB、文档 100 页、视频 3600 秒／100 帧、转换并发 32、超时 1 小时；整份输入限制始终生效。
- 转换共享 Agent 级名额，默认并发 1、不排队；忙时返回 `backpressure`。文本和无需转换的 PDF 不占名额。
- 每次转换独立临时目录，完成、失败、取消均清理；复用既有进程组／Windows Job 回收。输出读取有硬字节上限，拒绝符号链接输出。目录最多 512 个文件、128 MiB，每 25 毫秒监测并在退出后复查；这是监测限制，不是操作系统磁盘配额。
- Office 使用独立配置，提高宏安全级别并关闭文档链接更新；不执行调用方代码。FFmpeg 仅启用本地 `file,pipe` 协议，并强制 MOV/Matroska 解复用器，拒绝将播放列表当视频读取。
- 转换结果仅在所属 CLI 会话内缓存，最多 32 MiB，满时清空旧缓存；不跨租户共享。续接鉴权比较原始附件和标题，不使用有损转换结果。工具结果已经交付后转换失败会终止本轮，仍遵守既有不可重放规则。

错误分类：缺依赖 `capability_unsupported`；输入无效 `invalid_config`；超限 `response_too_large`；转换失败 `upstream_error`；超时 `timeout`；名额已满 `backpressure`。公开错误沿用 Gateway 通用文本。Agent 的 `Claude attachment processing failed` 日志仅含 `runtime_id` 和固定 `reason`，不记录原件内容、文件名、进程原始诊断或模型路径。

## 验证

默认测试覆盖模拟转换子进程、错误及资源边界、取消和清理、设置页保留配置，以及跨两个 Gateway、真实 mTLS 的 JSON/SSE、PDF/Office/视频工具续接。模拟测试不代表真实转换或语音识别效果。

已安装 CLI 的 PDF 传输契约测试只连接回环模拟 API、使用假 Key，不需要订阅或真实推理：

```bash
AISW_CLAUDE_CONTRACT_TEST=1 go test ./service/aiServeWeaveAgent/claudecode \
  -run '^TestNativeCLIDocumentContract$' -count=1 -timeout=60s -v
```

开发机 Claude Code 2.1.318 已通过该契约测试；它只证明 PDF 字节进入实际 CLI 的出站请求。真实 Office、Poppler、视频解码、Whisper 转录和 Claude 理解效果，以及目标系统原生执行和线上入口，仍待有依赖和登录的 Agent 实机验收。
