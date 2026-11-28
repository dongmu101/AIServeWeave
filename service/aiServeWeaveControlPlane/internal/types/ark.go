package types

import "time"

// ArkRequest saves a Volcengine Ark API key. APIKey is write-only: absent or
// null keeps the saved key, provided BaseURL stays the same. BaseURL may only
// be set on the platform key; empty means Ark's Beijing endpoint.
//
// ArkRequest 保存一个火山方舟 API Key。APIKey 只写不读：省略或为 null 表示保留已保存的
// Key，前提是 BaseURL 不变。BaseURL 只能在平台 Key 上设置，为空表示方舟北京地址。
type ArkRequest struct {
	APIKey  *string  `json:"api_key"`
	BaseURL string   `json:"base_url"`
	Models  []string `json:"models"`
}

// ArkCredential is a saved key without the key itself.
//
// ArkCredential 是不含 Key 本身的已保存 Key。
type ArkCredential struct {
	KeyLast4  string    `json:"key_last4"`
	BaseURL   string    `json:"base_url"`
	Models    []string  `json:"models"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ArkResponse answers GET /admin/v1/ark and GET /operator/v1/ark. Enabled is
// false when the deployment has no Ark master key. PlatformAvailable tells a
// tenant without its own key whether the platform's would be used.
//
// ArkResponse 是 GET /admin/v1/ark 与 GET /operator/v1/ark 的答复。部署没有方舟主密钥时
// Enabled 为 false。PlatformAvailable 告诉没有自己 Key 的租户，是否会改用平台 Key。
type ArkResponse struct {
	Enabled           bool           `json:"enabled"`
	Credential        *ArkCredential `json:"credential"`
	PlatformAvailable bool           `json:"platform_available"`
}

// ArkErrorResponse is a refused save or task call: Code is stable, Reason
// further classifies ark_unreachable.
//
// ArkErrorResponse 是被拒的保存或任务调用：Code 稳定不变，Reason 进一步说明 ark_unreachable。
type ArkErrorResponse struct {
	Error  string `json:"error"`
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
}

// ArkTask is one video generation task as Ark lists it. Times are absent
// when Ark did not report them.
//
// ArkTask 是方舟列出的一个视频生成任务。方舟没有报告时间时省略。
type ArkTask struct {
	ID               string     `json:"id"`
	Model            string     `json:"model"`
	Status           string     `json:"status"`
	ErrorCode        string     `json:"error_code,omitempty"`
	Resolution       string     `json:"resolution,omitempty"`
	Ratio            string     `json:"ratio,omitempty"`
	Duration         int64      `json:"duration,omitempty"`
	CompletionTokens int64      `json:"completion_tokens"`
	CreatedAt        *time.Time `json:"created_at,omitempty"`
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
}

// ArkTaskList is one page of tasks. / ArkTaskList 是任务的一页。
type ArkTaskList struct {
	Total    int       `json:"total"`
	PageNum  int       `json:"page_num"`
	PageSize int       `json:"page_size"`
	Items    []ArkTask `json:"items"`
}

// InternalArk is the key a tenant's video node uses, for the Gateway only.
// Configured is false when there is no key for the tenant; the other fields
// are then empty. Source is "tenant" or "platform".
//
// InternalArk 是租户视频节点使用的 Key，只给 Gateway。租户没有可用 Key 时 Configured 为
// false，其余字段为空。Source 为 "tenant" 或 "platform"。
type InternalArk struct {
	Configured bool     `json:"configured"`
	Source     string   `json:"source,omitempty"`
	Version    uint64   `json:"version,omitempty"`
	APIKey     string   `json:"api_key,omitempty"`
	BaseURL    string   `json:"base_url,omitempty"`
	Models     []string `json:"models,omitempty"`
}
