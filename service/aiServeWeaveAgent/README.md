# aiserveweave-agent

节点侧 Agent 主动出站连接 Gateway，通过 mTLS 隧道执行运行时语义请求，不监听公网推理入口。协议、连接状态机、配置下发与验收记录见 [隧道 README](tunnel/README.md)，启动和部署见 [部署说明](../../deploy/README.md)。

## 当前实现

- `main.go` 装配运行时管理器、后端工厂、自动发现、隧道和可选指标监听；`-version` 输出构建版本。
- `localdiscovery/` 默认探测本机 Ollama/vLLM，可用 `-auto-discover=false` 关闭；`-ollama-url` 可显式注册 Ollama。
- `tunnel/` 已实现身份注册/续期、多副本连接、槽池、心跳、运行时配置应用和流式请求/产物转发。执行 runtime 必须命中本地白名单。
- 后端适配在共享的 `common/runtime`，支持 Ollama、vLLM、SGLang 和 ComfyUI；ComfyUI 适配位于 `common/runtime/workflow/comfyui`。
- `workflow/` 仍是占位包。受控模板目录与输入绑定已在 Gateway 的 `workflow/` 实现，不能据此目录重复建设。

## 尚未交付的范围

运行时配置文件加载、Managed 后端安装/升级、GPU/系统资源采集、模型分发和 Agent 自动升级仍属规划。本机自动发现不等于进程生命周期管理；已有隧道文件转发不等于对象存储或原节点离线后的文件可用性。跨服务任务与验收统一见 [根 STATUS](../../STATUS.md)。
