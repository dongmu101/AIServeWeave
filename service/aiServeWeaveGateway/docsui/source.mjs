// Fixed Swagger UI 5.17.14 and OpenAPI display text.
// 固定的 Swagger UI 5.17.14 与 OpenAPI 展示文字。
export const SOURCES = [
  {
    "id": "oas/info/title",
    "source": "AIServeWeave Gateway API",
    "kind": "oas",
    "pointer": "/info/title"
  },
  {
    "id": "oas/info/description",
    "source": "aiServeWeaveGateway 对外暴露的两组 HTTP 接口：\n\n- **公开推理前门**（默认监听地址，需要租户 API Key）：OpenAI 兼容的\n  chat/embeddings/responses/models，以及工作流 Job 的提交、状态、SSE 事件流、\n  取消与产物下载。\n- **只读运维接口**（`-admin-addr` 单独配置时才挂载，使用部署密钥而非租户 Key）：\n  机群清单、工作流目录、按租户的 job 列表与路由生效状态，供控制面聚合展示，不做任何写操作。\n\n源码位置：`service/aiServeWeaveGateway/httpapi/`（公开前门）与\n`service/aiServeWeaveGateway/adminapi/`（运维接口）。\n",
    "kind": "oas",
    "pointer": "/info/description"
  },
  {
    "id": "oas/servers/0/description",
    "source": "公开推理前门（默认端口，可通过 -addr 配置）",
    "kind": "oas",
    "pointer": "/servers/0/description"
  },
  {
    "id": "oas/servers/1/description",
    "source": "只读运维接口（通过 -admin-addr 单独配置，未配置则不启动）",
    "kind": "oas",
    "pointer": "/servers/1/description"
  },
  {
    "id": "oas/tags/0/description",
    "source": "模型发现",
    "kind": "oas",
    "pointer": "/tags/0/description"
  },
  {
    "id": "oas/tags/1/description",
    "source": "OpenAI 兼容的 Chat Completions",
    "kind": "oas",
    "pointer": "/tags/1/description"
  },
  {
    "id": "oas/tags/2/description",
    "source": "OpenAI 兼容的 Embeddings",
    "kind": "oas",
    "pointer": "/tags/2/description"
  },
  {
    "id": "oas/tags/3/description",
    "source": "OpenAI 兼容的 Responses（转换为 canonical chat 请求）",
    "kind": "oas",
    "pointer": "/tags/3/description"
  },
  {
    "id": "oas/tags/4/description",
    "source": "工作流 Job 的提交、状态、事件、取消与产物",
    "kind": "oas",
    "pointer": "/tags/4/description"
  },
  {
    "id": "oas/tags/5/description",
    "source": "只读运维接口（机群清单 / 工作流目录 / 按租户 job），使用部署密钥",
    "kind": "oas",
    "pointer": "/tags/5/description"
  },
  {
    "id": "oas/paths/~1v1~1models/get/summary",
    "source": "列出当前至少一个已连接节点声明支持 chat 能力的模型",
    "kind": "oas",
    "pointer": "/paths/~1v1~1models/get/summary"
  },
  {
    "id": "oas/paths/~1v1~1models/get/responses/200/description",
    "source": "模型列表",
    "kind": "oas",
    "pointer": "/paths/~1v1~1models/get/responses/200/description"
  },
  {
    "id": "oas/paths/~1v1~1chat~1completions/post/summary",
    "source": "OpenAI 兼容的 Chat Completions（支持流式与非流式）",
    "kind": "oas",
    "pointer": "/paths/~1v1~1chat~1completions/post/summary"
  },
  {
    "id": "oas/paths/~1v1~1chat~1completions/post/responses/200/description",
    "source": "非流式：完整的 ChatCompletionResponse。\n流式（stream=true）：`text/event-stream`，逐帧 `data: <ChatCompletionChunk>`，\n以 `data: [DONE]` 结束。\n",
    "kind": "oas",
    "pointer": "/paths/~1v1~1chat~1completions/post/responses/200/description"
  },
  {
    "id": "oas/paths/~1v1~1chat~1completions/post/responses/200/content/text~1event-stream/schema/description",
    "source": "SSE 帧序列，每帧为 ChatCompletionChunk 的 JSON",
    "kind": "oas",
    "pointer": "/paths/~1v1~1chat~1completions/post/responses/200/content/text~1event-stream/schema/description"
  },
  {
    "id": "oas/paths/~1v1~1embeddings/post/summary",
    "source": "OpenAI 兼容的 Embeddings",
    "kind": "oas",
    "pointer": "/paths/~1v1~1embeddings/post/summary"
  },
  {
    "id": "oas/paths/~1v1~1embeddings/post/responses/200/description",
    "source": "向量列表",
    "kind": "oas",
    "pointer": "/paths/~1v1~1embeddings/post/responses/200/description"
  },
  {
    "id": "oas/paths/~1v1~1responses/post/summary",
    "source": "OpenAI 兼容的 Responses（边界处转换为 canonical chat 请求）",
    "kind": "oas",
    "pointer": "/paths/~1v1~1responses/post/summary"
  },
  {
    "id": "oas/paths/~1v1~1responses/post/description",
    "source": "只服务无状态的一次性生成：`previous_response_id`、`store=true`、\n`background=true` 均被拒绝（400），因为本 Gateway 不持有已存储的会话，\n也不做后台任务。内置工具（web_search 等）与非 function 类型的工具同样被拒绝。\n",
    "kind": "oas",
    "pointer": "/paths/~1v1~1responses/post/description"
  },
  {
    "id": "oas/paths/~1v1~1responses/post/responses/200/description",
    "source": "非流式：完整的 Response 对象。流式（stream=true）：`text/event-stream`。\n",
    "kind": "oas",
    "pointer": "/paths/~1v1~1responses/post/responses/200/description"
  },
  {
    "id": "oas/paths/~1v1~1workflows~1{workflow_id}~1runs/post/summary",
    "source": "提交一次工作流运行",
    "kind": "oas",
    "pointer": "/paths/~1v1~1workflows~1{workflow_id}~1runs/post/summary"
  },
  {
    "id": "oas/paths/~1v1~1workflows~1{workflow_id}~1runs/post/description",
    "source": "请求体只携带模板已声明的输入值；图本身来自服务端注册的模板，调用方无法传入\n任意图。\n\n模板不声明文件类型输入（`type: file`）时用 `application/json`，请求体上限\n1 MiB。模板声明了文件类型输入时（STATUS.md 的 P04）改用\n`multipart/form-data`：`inputs` 表单字段携带与 JSON 形态相同的请求体，其余\n每个表单字段名对应一个已声明的文件输入名、值为该文件的字节；整个请求上限\n200 MiB。字节先被 Gateway 挑中的节点接收，再经该节点写入其本地 ComfyUI 的\n输入目录——因此含文件输入的提交固定在单个候选节点上，不做跨节点重试：那个\n节点的分发失败即整体失败，不会换一个节点重新上传文件。\n",
    "kind": "oas",
    "pointer": "/paths/~1v1~1workflows~1{workflow_id}~1runs/post/description"
  },
  {
    "id": "oas/paths/~1v1~1workflows~1{workflow_id}~1runs/post/requestBody/content/multipart~1form-data/schema/properties/inputs/description",
    "source": "与 application/json 形态相同的请求体，作为 JSON 字符串放进这个表单字段",
    "kind": "oas",
    "pointer": "/paths/~1v1~1workflows~1{workflow_id}~1runs/post/requestBody/content/multipart~1form-data/schema/properties/inputs/description"
  },
  {
    "id": "oas/paths/~1v1~1workflows~1{workflow_id}~1runs/post/requestBody/content/multipart~1form-data/schema/additionalProperties/description",
    "source": "表单字段名即模板声明的文件输入名，值为该文件的字节",
    "kind": "oas",
    "pointer": "/paths/~1v1~1workflows~1{workflow_id}~1runs/post/requestBody/content/multipart~1form-data/schema/additionalProperties/description"
  },
  {
    "id": "oas/paths/~1v1~1workflows~1{workflow_id}~1runs/post/responses/202/description",
    "source": "已接受，返回刚创建的 job（状态多为 queued）",
    "kind": "oas",
    "pointer": "/paths/~1v1~1workflows~1{workflow_id}~1runs/post/responses/202/description"
  },
  {
    "id": "oas/paths/~1v1~1workflows~1{workflow_id}~1runs/post/responses/404/description",
    "source": "工作流模板未注册，或没有节点能运行工作流",
    "kind": "oas",
    "pointer": "/paths/~1v1~1workflows~1{workflow_id}~1runs/post/responses/404/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}/get/summary",
    "source": "查询一次运行的状态",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}/get/summary"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}/get/description",
    "source": "已结束的运行直接由本 Gateway 的内存 job 表作答；仍在进行中的会去问运行它的\n节点，状态来自后端的队列与历史。\n",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}/get/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}/get/responses/200/description",
    "source": "job 当前视图",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}/get/responses/200/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1events/get/summary",
    "source": "订阅一次运行的事件流（SSE）",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1events/get/summary"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1events/get/description",
    "source": "已经结束的运行会立即得到携带终态的单独一帧而不是订阅。事件名（SSE 的\n`event:` 字段）为后端事件类型（如 progress、node_output、succeeded、\nfailed、cancelled 等），载荷为 JobEvent。\n",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1events/get/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1events/get/responses/200/description",
    "source": "text/event-stream，逐帧 JobEvent",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1events/get/responses/200/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1cancel/post/summary",
    "source": "请求取消一次运行",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1cancel/post/summary"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1cancel/post/description",
    "source": "取消是异步请求而不是结论：本端点返回 202 与最后已知的 job 视图，真实结果由\n状态端点或事件流报告。已结束的 job 返回 409；节点不支持中断时返回 501。\n",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1cancel/post/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1cancel/post/responses/202/description",
    "source": "取消请求已接受",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1cancel/post/responses/202/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1cancel/post/responses/409/description",
    "source": "job 已结束，无法取消",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1cancel/post/responses/409/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1cancel/post/responses/501/description",
    "source": "运行该 job 的节点不支持中断",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1cancel/post/responses/501/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1artifacts/get/summary",
    "source": "列出一次运行产出的产物",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1artifacts/get/summary"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1artifacts/get/description",
    "source": "公开的 artifact_id 在这里铸造：后端用 filename/subfolder/type 三元组定位\n文件，该三元组从不作为标识符外传。同一 job 多次列举返回相同的 id。\n",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1artifacts/get/description"
  },
  {
    "id": "oas/paths/~1v1~1jobs~1{job_id}~1artifacts/get/responses/200/description",
    "source": "产物列表",
    "kind": "oas",
    "pointer": "/paths/~1v1~1jobs~1{job_id}~1artifacts/get/responses/200/description"
  },
  {
    "id": "oas/paths/~1v1~1artifacts~1{artifact_id}/get/summary",
    "source": "下载一个产物",
    "kind": "oas",
    "pointer": "/paths/~1v1~1artifacts~1{artifact_id}/get/summary"
  },
  {
    "id": "oas/paths/~1v1~1artifacts~1{artifact_id}/get/description",
    "source": "响应体直通转发，从隧道边读边送，从不在 Gateway 进程内整体缓冲。",
    "kind": "oas",
    "pointer": "/paths/~1v1~1artifacts~1{artifact_id}/get/description"
  },
  {
    "id": "oas/paths/~1v1~1artifacts~1{artifact_id}/get/responses/200/description",
    "source": "产物内容（Content-Type 与 Content-Disposition 视后端而定）",
    "kind": "oas",
    "pointer": "/paths/~1v1~1artifacts~1{artifact_id}/get/responses/200/description"
  },
  {
    "id": "oas/paths/~1internal~1v1~1routes/get/summary",
    "source": "本副本实际应用的路由版本与最近同步状态",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1routes/get/summary"
  },
  {
    "id": "oas/paths/~1internal~1v1~1routes/get/description",
    "source": "仅在运维监听器提供；文件模式不会被视为控制面版本确认。错误为固定代号，不包含配置或凭据。",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1routes/get/description"
  },
  {
    "id": "oas/paths/~1internal~1v1~1routes/get/responses/200/description",
    "source": "路由生效状态",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1routes/get/responses/200/description"
  },
  {
    "id": "oas/paths/~1internal~1v1~1nodes/get/summary",
    "source": "本副本当前看到的机群清单",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1nodes/get/summary"
  },
  {
    "id": "oas/paths/~1internal~1v1~1nodes/get/responses/200/description",
    "source": "机群快照",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1nodes/get/responses/200/description"
  },
  {
    "id": "oas/paths/~1internal~1v1~1workflows/get/summary",
    "source": "本副本注册的工作流模板目录（未配置模板目录时不挂载此路由）",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1workflows/get/summary"
  },
  {
    "id": "oas/paths/~1internal~1v1~1workflows/get/responses/200/description",
    "source": "模板目录",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1workflows/get/responses/200/description"
  },
  {
    "id": "oas/paths/~1internal~1v1~1jobs/get/summary",
    "source": "本副本上某一个租户的运行列表（未启用前门时不挂载此路由）",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1jobs/get/summary"
  },
  {
    "id": "oas/paths/~1internal~1v1~1jobs/get/description",
    "source": "tenant_id 为必填查询参数；缺省时返回 400，而不是返回全部租户的运行。",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1jobs/get/description"
  },
  {
    "id": "oas/paths/~1internal~1v1~1jobs/get/responses/200/description",
    "source": "该租户在本副本上的运行",
    "kind": "oas",
    "pointer": "/paths/~1internal~1v1~1jobs/get/responses/200/description"
  },
  {
    "id": "oas/components/securitySchemes/bearerAuth/description",
    "source": "租户 API Key（Authorization: Bearer <key>）。未配置任何鉴权方式的部署会放行一切请求。",
    "kind": "oas",
    "pointer": "/components/securitySchemes/bearerAuth/description"
  },
  {
    "id": "oas/components/securitySchemes/adminToken/description",
    "source": "部署级运维密钥（AISW_GATEWAY_ADMIN_TOKEN），与租户 API Key 无关。",
    "kind": "oas",
    "pointer": "/components/securitySchemes/adminToken/description"
  },
  {
    "id": "oas/components/responses/OpenAIError/description",
    "source": "OpenAI 兼容的错误体",
    "kind": "oas",
    "pointer": "/components/responses/OpenAIError/description"
  },
  {
    "id": "oas/components/responses/AdminError/description",
    "source": "运维接口的错误体",
    "kind": "oas",
    "pointer": "/components/responses/AdminError/description"
  },
  {
    "id": "oas/components/schemas/Tool/properties/function/properties/parameters/description",
    "source": "JSON Schema，原样透传",
    "kind": "oas",
    "pointer": "/components/schemas/Tool/properties/function/properties/parameters/description"
  },
  {
    "id": "oas/components/schemas/ChatCompletionRequest/properties/stop/description",
    "source": "单个字符串或字符串数组",
    "kind": "oas",
    "pointer": "/components/schemas/ChatCompletionRequest/properties/stop/description"
  },
  {
    "id": "oas/components/schemas/ChatCompletionRequest/properties/tool_choice/description",
    "source": "字符串（auto/none/required）或指名单个工具的对象",
    "kind": "oas",
    "pointer": "/components/schemas/ChatCompletionRequest/properties/tool_choice/description"
  },
  {
    "id": "oas/components/schemas/EmbeddingsRequest/properties/input/description",
    "source": "单个字符串或字符串数组",
    "kind": "oas",
    "pointer": "/components/schemas/EmbeddingsRequest/properties/input/description"
  },
  {
    "id": "oas/components/schemas/ResponsesRequest/properties/input/description",
    "source": "单个字符串或输入项数组（每项含 role 与 content）",
    "kind": "oas",
    "pointer": "/components/schemas/ResponsesRequest/properties/input/description"
  },
  {
    "id": "oas/components/schemas/ResponsesRequest/properties/tool_choice/description",
    "source": "字符串或指名单个工具的对象",
    "kind": "oas",
    "pointer": "/components/schemas/ResponsesRequest/properties/tool_choice/description"
  },
  {
    "id": "oas/components/schemas/ResponsesRequest/properties/previous_response_id/description",
    "source": "不支持，非空即 400",
    "kind": "oas",
    "pointer": "/components/schemas/ResponsesRequest/properties/previous_response_id/description"
  },
  {
    "id": "oas/components/schemas/ResponsesRequest/properties/store/description",
    "source": "不支持，true 即 400",
    "kind": "oas",
    "pointer": "/components/schemas/ResponsesRequest/properties/store/description"
  },
  {
    "id": "oas/components/schemas/ResponsesRequest/properties/background/description",
    "source": "不支持，true 即 400",
    "kind": "oas",
    "pointer": "/components/schemas/ResponsesRequest/properties/background/description"
  },
  {
    "id": "oas/components/schemas/RunRequest/properties/inputs/description",
    "source": "模板声明过的输入名到取值（原样 JSON）",
    "kind": "oas",
    "pointer": "/components/schemas/RunRequest/properties/inputs/description"
  },
  {
    "id": "oas/components/schemas/JobEvent/properties/data/description",
    "source": "后端原始载荷，逐事件类型而不同",
    "kind": "oas",
    "pointer": "/components/schemas/JobEvent/properties/data/description"
  },
  {
    "id": "oas/components/schemas/ArtifactList/properties/data/items/properties/type/description",
    "source": "后端分区名（output/temp/input）",
    "kind": "oas",
    "pointer": "/components/schemas/ArtifactList/properties/data/items/properties/type/description"
  },
  {
    "id": "oas/components/schemas/Replica/properties/local_runtimes/description",
    "source": "Backends discovered on this Gateway's loopback interface; inventory only.",
    "kind": "oas",
    "pointer": "/components/schemas/Replica/properties/local_runtimes/description"
  },
  {
    "id": "oas/components/schemas/Node/properties/idle_slots/description",
    "source": "槽位 class（inference/bulk）到空闲数量",
    "kind": "oas",
    "pointer": "/components/schemas/Node/properties/idle_slots/description"
  },
  {
    "id": "ui.snippets",
    "source": "Snippets",
    "kind": "control"
  },
  {
    "id": "ui.schemas",
    "source": "Schemas",
    "kind": "control"
  },
  {
    "id": "ui.models",
    "source": "Models",
    "kind": "control"
  },
  {
    "id": "ui.schemes",
    "source": "Schemes",
    "kind": "control"
  },
  {
    "id": "ui.addItem",
    "source": "Add item",
    "kind": "control"
  },
  {
    "id": "ui.editValue",
    "source": "Edit Value",
    "kind": "control"
  },
  {
    "id": "ui.exampleValue",
    "source": "Example Value",
    "kind": "control"
  },
  {
    "id": "ui.schema",
    "source": "Schema",
    "kind": "control"
  },
  {
    "id": "ui.model",
    "source": "Model",
    "kind": "control"
  },
  {
    "id": "ui.noExample",
    "source": "(no example available",
    "kind": "control"
  },
  {
    "id": "ui.enum",
    "source": "Enum:",
    "kind": "control"
  },
  {
    "id": "ui.download",
    "source": "Download",
    "kind": "control"
  },
  {
    "id": "ui.renderFailure",
    "source": "Could not render {component}, see the console.",
    "kind": "control"
  },
  {
    "id": "ui.noLayout",
    "source": " No layout defined for \"{layout}\" ",
    "kind": "control"
  },
  {
    "id": "ui.availableAuthorizations",
    "source": "Available authorizations",
    "kind": "control"
  },
  {
    "id": "ui.authorize",
    "source": "Authorize",
    "kind": "control"
  },
  {
    "id": "ui.removeAuthorization",
    "source": "Remove authorization",
    "kind": "control"
  },
  {
    "id": "ui.logout",
    "source": "Logout",
    "kind": "control"
  },
  {
    "id": "ui.applyCredentials",
    "source": "Apply credentials",
    "kind": "control"
  },
  {
    "id": "ui.close",
    "source": "Close",
    "kind": "control"
  },
  {
    "id": "ui.scopesHelp",
    "source": "Scopes are used to grant an application different levels of access to data on behalf of the end user. Each API may declare one or more scopes.",
    "kind": "control"
  },
  {
    "id": "ui.scopesRequired",
    "source": "API requires the following scopes. Select which ones you want to grant to Swagger UI.",
    "kind": "control"
  },
  {
    "id": "ui.unknownSecurityType",
    "source": "Unknown security definition type {type}",
    "kind": "control"
  },
  {
    "id": "ui.authorized",
    "source": "Authorized",
    "kind": "control"
  },
  {
    "id": "ui.authName",
    "source": "Name: ",
    "kind": "control"
  },
  {
    "id": "ui.authIn",
    "source": "In: ",
    "kind": "control"
  },
  {
    "id": "ui.authValue",
    "source": "Value:",
    "kind": "control"
  },
  {
    "id": "ui.basicAuthorization",
    "source": "Basic authorization",
    "kind": "control"
  },
  {
    "id": "ui.username",
    "source": "Username:",
    "kind": "control"
  },
  {
    "id": "ui.password",
    "source": "Password:",
    "kind": "control"
  },
  {
    "id": "ui.examplesLabel",
    "source": "Examples: ",
    "kind": "control"
  },
  {
    "id": "ui.modifiedValue",
    "source": "[Modified value]",
    "kind": "control"
  },
  {
    "id": "ui.application",
    "source": "Application: ",
    "kind": "control"
  },
  {
    "id": "ui.openIdUrl",
    "source": "OpenID Connect URL: ",
    "kind": "control"
  },
  {
    "id": "ui.authorizationUrl",
    "source": "Authorization URL: ",
    "kind": "control"
  },
  {
    "id": "ui.tokenUrl",
    "source": "Token URL:",
    "kind": "control"
  },
  {
    "id": "ui.flow",
    "source": "Flow: ",
    "kind": "control"
  },
  {
    "id": "ui.usernameLower",
    "source": "username:",
    "kind": "control"
  },
  {
    "id": "ui.passwordLower",
    "source": "password:",
    "kind": "control"
  },
  {
    "id": "ui.clientCredentialsLocation",
    "source": "Client credentials location:",
    "kind": "control"
  },
  {
    "id": "ui.authorizationHeader",
    "source": "Authorization header",
    "kind": "control"
  },
  {
    "id": "ui.requestBody",
    "source": "Request body",
    "kind": "control"
  },
  {
    "id": "ui.scopes",
    "source": "Scopes:",
    "kind": "control"
  },
  {
    "id": "ui.selectAll",
    "source": "select all",
    "kind": "control"
  },
  {
    "id": "ui.selectNone",
    "source": "select none",
    "kind": "control"
  },
  {
    "id": "ui.applyOAuth2",
    "source": "Apply given OAuth2 credentials",
    "kind": "control"
  },
  {
    "id": "ui.clear",
    "source": "Clear",
    "kind": "control"
  },
  {
    "id": "ui.responseHeaders",
    "source": "Response headers",
    "kind": "control"
  },
  {
    "id": "ui.requestDuration",
    "source": "Request duration",
    "kind": "control"
  },
  {
    "id": "ui.requestUrl",
    "source": "Request URL",
    "kind": "control"
  },
  {
    "id": "ui.serverResponse",
    "source": "Server response",
    "kind": "control"
  },
  {
    "id": "ui.code",
    "source": "Code",
    "kind": "control"
  },
  {
    "id": "ui.details",
    "source": "Details",
    "kind": "control"
  },
  {
    "id": "ui.undocumented",
    "source": " Undocumented ",
    "kind": "control"
  },
  {
    "id": "ui.validatorBadge",
    "source": "Online validator badge",
    "kind": "control"
  },
  {
    "id": "ui.error",
    "source": "Error",
    "kind": "control"
  },
  {
    "id": "ui.noOperations",
    "source": " No operations defined in spec!",
    "kind": "control"
  },
  {
    "id": "ui.noOperationsSpaced",
    "source": " No operations defined in spec! ",
    "kind": "control"
  },
  {
    "id": "ui.deprecatedWarning",
    "source": " Warning: Deprecated",
    "kind": "control"
  },
  {
    "id": "ui.findDetails",
    "source": "Find more details",
    "kind": "control"
  },
  {
    "id": "ui.correctValidation",
    "source": "Please correct the following validation errors and try again.",
    "kind": "control"
  },
  {
    "id": "ui.responses",
    "source": "Responses",
    "kind": "control"
  },
  {
    "id": "ui.responseContentType",
    "source": "Response content type",
    "kind": "control"
  },
  {
    "id": "ui.description",
    "source": "Description",
    "kind": "control"
  },
  {
    "id": "ui.links",
    "source": "Links",
    "kind": "control"
  },
  {
    "id": "ui.mediaType",
    "source": "Media type",
    "kind": "control"
  },
  {
    "id": "ui.controlsHeader",
    "source": "Controls {header} header.",
    "kind": "control"
  },
  {
    "id": "ui.examples",
    "source": "Examples",
    "kind": "control"
  },
  {
    "id": "ui.noLinks",
    "source": "No links",
    "kind": "control"
  },
  {
    "id": "ui.downloadFile",
    "source": "Download file",
    "kind": "control"
  },
  {
    "id": "ui.downloadUnsupported",
    "source": "Download headers detected but your browser does not support downloading binary via XHR (Blob).",
    "kind": "control"
  },
  {
    "id": "ui.responseAsText",
    "source": "Unrecognized response type; displaying content as text.",
    "kind": "control"
  },
  {
    "id": "ui.responseUnsupported",
    "source": "Unrecognized response type; unable to display.",
    "kind": "control"
  },
  {
    "id": "ui.responseBody",
    "source": "Response body",
    "kind": "control"
  },
  {
    "id": "ui.parameters",
    "source": "Parameters",
    "kind": "control"
  },
  {
    "id": "ui.callbacks",
    "source": "Callbacks",
    "kind": "control"
  },
  {
    "id": "ui.name",
    "source": "Name",
    "kind": "control"
  },
  {
    "id": "ui.noParameters",
    "source": "No parameters",
    "kind": "control"
  },
  {
    "id": "ui.requestContentType",
    "source": "Request content type",
    "kind": "control"
  },
  {
    "id": "ui.sendEmptyValue",
    "source": "Send empty value",
    "kind": "control"
  },
  {
    "id": "ui.deprecated",
    "source": "deprecated",
    "kind": "control"
  },
  {
    "id": "ui.formDataUnsupported",
    "source": "Error: your browser does not support FormData",
    "kind": "control"
  },
  {
    "id": "ui.execute",
    "source": "Execute",
    "kind": "control"
  },
  {
    "id": "ui.headers",
    "source": "Headers:",
    "kind": "control"
  },
  {
    "id": "ui.type",
    "source": "Type",
    "kind": "control"
  },
  {
    "id": "ui.errors",
    "source": "Errors",
    "kind": "control"
  },
  {
    "id": "ui.hide",
    "source": "Hide",
    "kind": "control"
  },
  {
    "id": "ui.show",
    "source": "Show",
    "kind": "control"
  },
  {
    "id": "ui.jumpToLine",
    "source": "Jump to line {line}",
    "kind": "control"
  },
  {
    "id": "ui.atPath",
    "source": "at {path}",
    "kind": "control"
  },
  {
    "id": "ui.onLine",
    "source": "on line {line}",
    "kind": "control"
  },
  {
    "id": "ui.overview",
    "source": "Overview",
    "kind": "control"
  },
  {
    "id": "ui.baseUrl",
    "source": "[ Base URL: {url} ]",
    "kind": "control"
  },
  {
    "id": "ui.terms",
    "source": "Terms of service",
    "kind": "control"
  },
  {
    "id": "ui.website",
    "source": " - Website",
    "kind": "control"
  },
  {
    "id": "ui.sendEmail",
    "source": "Send email to {email}",
    "kind": "control"
  },
  {
    "id": "ui.contactName",
    "source": "Contact {name}",
    "kind": "control"
  },
  {
    "id": "ui.contact",
    "source": "Contact",
    "kind": "control"
  },
  {
    "id": "ui.license",
    "source": "License",
    "kind": "control"
  },
  {
    "id": "ui.copyClipboard",
    "source": "Copy to clipboard",
    "kind": "control"
  },
  {
    "id": "ui.filterTag",
    "source": "Filter by tag",
    "kind": "control"
  },
  {
    "id": "ui.cancel",
    "source": "Cancel",
    "kind": "control"
  },
  {
    "id": "ui.edit",
    "source": "Edit",
    "kind": "control"
  },
  {
    "id": "ui.parameterContentType",
    "source": "Parameter content type",
    "kind": "control"
  },
  {
    "id": "ui.curl",
    "source": "Curl",
    "kind": "control"
  },
  {
    "id": "ui.tryItOut",
    "source": "Try it out ",
    "kind": "control"
  },
  {
    "id": "ui.reset",
    "source": "Reset",
    "kind": "control"
  },
  {
    "id": "ui.renderDefinition",
    "source": "Unable to render this definition",
    "kind": "control"
  },
  {
    "id": "ui.conflictingVersions",
    "source": "swagger and openapi fields cannot be present in the same Swagger or OpenAPI definition. Please remove one of the fields.",
    "kind": "control"
  },
  {
    "id": "ui.supportedVersions",
    "source": "Supported version fields are {versions}",
    "kind": "control"
  },
  {
    "id": "ui.invalidVersion",
    "source": "The provided definition does not specify a valid version field.",
    "kind": "control"
  },
  {
    "id": "ui.provideVersion",
    "source": "Please indicate a valid Swagger or OpenAPI version field. Supported version fields are {versions}",
    "kind": "control"
  },
  {
    "id": "ui.failedApiDefinition",
    "source": "Failed to load API definition.",
    "kind": "control"
  },
  {
    "id": "ui.failedConfiguration",
    "source": "Failed to load remote configuration.",
    "kind": "control"
  },
  {
    "id": "ui.noApiDefinition",
    "source": "No API definition provided.",
    "kind": "control"
  },
  {
    "id": "ui.extensions",
    "source": "Extensions",
    "kind": "control"
  },
  {
    "id": "ui.field",
    "source": "Field",
    "kind": "control"
  },
  {
    "id": "ui.value",
    "source": "Value",
    "kind": "control"
  },
  {
    "id": "ui.exampleDescription",
    "source": "Example Description",
    "kind": "control"
  },
  {
    "id": "ui.operation",
    "source": "Operation `{operation}`",
    "kind": "control"
  },
  {
    "id": "ui.servers",
    "source": "Servers",
    "kind": "control"
  },
  {
    "id": "ui.basicUsername",
    "source": "auth-basic-username",
    "kind": "control"
  },
  {
    "id": "ui.basicPassword",
    "source": "auth-basic-password",
    "kind": "control"
  },
  {
    "id": "ui.bearerValue",
    "source": "auth-bearer-value",
    "kind": "control"
  },
  {
    "id": "ui.unsupportedAuthScheme",
    "source": " HTTP authentication: unsupported scheme '{scheme}'",
    "kind": "control"
  },
  {
    "id": "ui.serverOverride",
    "source": "These {level}-level options override the global server options.",
    "kind": "control"
  },
  {
    "id": "ui.noCallbacks",
    "source": "No callbacks",
    "kind": "control"
  },
  {
    "id": "ui.noMediaExamples",
    "source": "Example values are not available for {mediaType} media types.",
    "kind": "control"
  },
  {
    "id": "ui.computedUrl",
    "source": "Computed URL:",
    "kind": "control"
  },
  {
    "id": "ui.serverVariables",
    "source": "Server variables",
    "kind": "control"
  },
  {
    "id": "ui.deprecatedLabel",
    "source": "Deprecated:",
    "kind": "control"
  },
  {
    "id": "ui.webhooks",
    "source": "Webhooks",
    "kind": "control"
  },
  {
    "id": "ui.schemaDialect",
    "source": "JSON Schema dialect:",
    "kind": "control"
  },
  {
    "id": "ui.warning",
    "source": "Warning",
    "kind": "control"
  },
  {
    "id": "ui.unsupportedDialect",
    "source": "OpenAPI.jsonSchemaDialect field contains a value different from the default value of {defaultValue}. Values different from the default one are currently not supported. Please either omit the field or provide it with the default value.",
    "kind": "control"
  },
  {
    "id": "ui.mutualTls",
    "source": "Mutual TLS is required by this API/Operation. Certificates are managed via your Operating System and/or your browser.",
    "kind": "control"
  },
  {
    "id": "ui.example",
    "source": "Example",
    "kind": "control"
  },
  {
    "id": "ui.discriminator",
    "source": "Discriminator",
    "kind": "control"
  },
  {
    "id": "ui.externalDocumentation",
    "source": "External documentation",
    "kind": "control"
  },
  {
    "id": "ui.allOf",
    "source": "All of",
    "kind": "control"
  },
  {
    "id": "ui.anyOf",
    "source": "Any of",
    "kind": "control"
  },
  {
    "id": "ui.oneOf",
    "source": "One of",
    "kind": "control"
  },
  {
    "id": "ui.not",
    "source": "Not",
    "kind": "control"
  },
  {
    "id": "ui.if",
    "source": "If",
    "kind": "control"
  },
  {
    "id": "ui.then",
    "source": "Then",
    "kind": "control"
  },
  {
    "id": "ui.else",
    "source": "Else",
    "kind": "control"
  },
  {
    "id": "ui.dependentSchemas",
    "source": "Dependent schemas",
    "kind": "control"
  },
  {
    "id": "ui.prefixItems",
    "source": "Prefix items",
    "kind": "control"
  },
  {
    "id": "ui.items",
    "source": "Items",
    "kind": "control"
  },
  {
    "id": "ui.contains",
    "source": "Contains",
    "kind": "control"
  },
  {
    "id": "ui.additionalProperties",
    "source": "Additional properties",
    "kind": "control"
  },
  {
    "id": "ui.allowed",
    "source": "allowed",
    "kind": "control"
  },
  {
    "id": "ui.forbidden",
    "source": "forbidden",
    "kind": "control"
  },
  {
    "id": "ui.propertyNames",
    "source": "Property names",
    "kind": "control"
  },
  {
    "id": "ui.unevaluatedItems",
    "source": "Unevaluated items",
    "kind": "control"
  },
  {
    "id": "ui.unevaluatedProperties",
    "source": "Unevaluated properties",
    "kind": "control"
  },
  {
    "id": "ui.circular",
    "source": " [circular]",
    "kind": "control"
  },
  {
    "id": "ui.allowedValues",
    "source": "Allowed values",
    "kind": "control"
  },
  {
    "id": "ui.const",
    "source": "Const",
    "kind": "control"
  },
  {
    "id": "ui.requiredWhenDefined",
    "source": "Required when defined",
    "kind": "control"
  },
  {
    "id": "ui.contentSchema",
    "source": "Content schema",
    "kind": "control"
  },
  {
    "id": "ui.default",
    "source": "Default",
    "kind": "control"
  },
  {
    "id": "ui.readOnly",
    "source": "read-only",
    "kind": "control"
  },
  {
    "id": "ui.writeOnly",
    "source": "write-only",
    "kind": "control"
  },
  {
    "id": "ui.collapseAll",
    "source": "Collapse all",
    "kind": "control"
  },
  {
    "id": "ui.expandAll",
    "source": "Expand all",
    "kind": "control"
  },
  {
    "id": "ui.collapseOperation",
    "source": "Collapse operation",
    "kind": "control"
  },
  {
    "id": "ui.expandOperation",
    "source": "Expand operation",
    "kind": "control"
  },
  {
    "id": "ui.collapse",
    "source": "Collapse",
    "kind": "control"
  },
  {
    "id": "ui.select",
    "source": "Select",
    "kind": "control"
  },
  {
    "id": "ui.lockedAuthorization",
    "source": "authorization button locked",
    "kind": "control"
  },
  {
    "id": "ui.unlockedAuthorization",
    "source": "authorization button unlocked",
    "kind": "control"
  },
  {
    "id": "ui.selectDefinition",
    "source": "Select a definition",
    "kind": "control"
  },
  {
    "id": "ui.explore",
    "source": "Explore",
    "kind": "control"
  },
  {
    "id": "ui.loading",
    "source": "loading",
    "kind": "control"
  },
  {
    "id": "ui.required",
    "source": "required",
    "kind": "control"
  },
  {
    "id": "ui.chooseFile",
    "source": "Choose File",
    "kind": "control"
  },
  {
    "id": "ui.noFile",
    "source": "No file chosen",
    "kind": "control"
  },
  {
    "id": "ui.failedFetch",
    "source": "Failed to fetch",
    "kind": "control"
  },
  {
    "id": "ui.failedFetchReasons",
    "source": "**Failed to fetch.**  \n**Possible Reasons:** \n  - CORS \n  - Network Failure \n  - URL scheme must be \"http\" or \"https\" for CORS request.",
    "kind": "control"
  },
  {
    "id": "ui.requiredField",
    "source": "Required field is not provided",
    "kind": "control"
  },
  {
    "id": "ui.validJson",
    "source": "Parameter string value must be valid JSON",
    "kind": "control"
  },
  {
    "id": "ui.requiredProperty",
    "source": "Required property not found",
    "kind": "control"
  },
  {
    "id": "ui.valuePattern",
    "source": "Value must follow pattern {pattern}",
    "kind": "control"
  },
  {
    "id": "ui.arrayMinOne",
    "source": "Array must contain at least {count} item",
    "kind": "control"
  },
  {
    "id": "ui.arrayMinMany",
    "source": "Array must contain at least {count} items",
    "kind": "control"
  },
  {
    "id": "ui.arrayMaxOne",
    "source": "Array must not contain more then {count} item",
    "kind": "control"
  },
  {
    "id": "ui.arrayMaxMany",
    "source": "Array must not contain more then {count} items",
    "kind": "control"
  },
  {
    "id": "ui.uniqueItems",
    "source": "No duplicates allowed.",
    "kind": "control"
  },
  {
    "id": "ui.valueMaxLengthOne",
    "source": "Value must be no longer than {count} character",
    "kind": "control"
  },
  {
    "id": "ui.valueMaxLengthMany",
    "source": "Value must be no longer than {count} characters",
    "kind": "control"
  },
  {
    "id": "ui.valueMinLengthOne",
    "source": "Value must be at least {count} character",
    "kind": "control"
  },
  {
    "id": "ui.valueMinLengthMany",
    "source": "Value must be at least {count} characters",
    "kind": "control"
  },
  {
    "id": "ui.valueLess",
    "source": "Value must be less than {limit}",
    "kind": "control"
  },
  {
    "id": "ui.valueGreater",
    "source": "Value must be greater than {limit}",
    "kind": "control"
  },
  {
    "id": "ui.valueDateTime",
    "source": "Value must be a DateTime",
    "kind": "control"
  },
  {
    "id": "ui.valueGuid",
    "source": "Value must be a Guid",
    "kind": "control"
  },
  {
    "id": "ui.valueString",
    "source": "Value must be a string",
    "kind": "control"
  },
  {
    "id": "ui.valueBoolean",
    "source": "Value must be a boolean",
    "kind": "control"
  },
  {
    "id": "ui.valueNumber",
    "source": "Value must be a number",
    "kind": "control"
  },
  {
    "id": "ui.valueInteger",
    "source": "Value must be an integer",
    "kind": "control"
  },
  {
    "id": "ui.valueFile",
    "source": "Value must be a file",
    "kind": "control"
  },
  {
    "id": "ui.addTypedItem",
    "source": "Add {type} item",
    "kind": "control"
  },
  {
    "id": "ui.availableValues",
    "source": "Available values",
    "kind": "control"
  },
  {
    "id": "ui.defaultValue",
    "source": "Default value",
    "kind": "control"
  }
];

export const SPEC_SHA256 = "bf723bc876b23f7262a179edbdd46365f92854493b33d99a620c359557b631c4";
export const BUNDLE_SHA256 = "92b9842ad2dc4d85fd74bbd3d4ca85147777bba34751d0e6a45d52493c8d6219";
