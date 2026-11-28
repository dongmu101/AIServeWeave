// Package arkvideo calls Volcengine Ark's video generation tasks — create,
// get, list and cancel-or-delete — through github.com/volcengine/ark-runtime-go,
// and fetches the finished video. It is shared by the control plane (key
// probe, task list, cancel) and the Gateway (the Studio video node).
//
// Only this package's own types cross its boundary: the SDK's generated Opt*
// wrappers stay inside. Errors are classified into a closed set of kinds and
// never carry the upstream's message text, which may quote the prompt.
//
// Creating a task is not idempotent, so the client that creates tasks never
// retries: a retried create after a lost response would start, and bill, a
// second task. Reads and deletes keep the SDK's retries.
//
// Package arkvideo 通过 github.com/volcengine/ark-runtime-go 调用火山方舟的视频生成任务
// （创建、查询、列表、取消或删除），并下载生成好的视频。控制面（检查 Key、任务列表、取消）
// 与 Gateway（Studio 视频节点）共用。
//
// 越过包边界的只有本包自己的类型，SDK 生成的 Opt* 包装留在包内。错误归入封闭的几类，
// 从不携带上游的消息原文（其中可能引用提示词）。
//
// 创建任务不是幂等的，因此负责创建的客户端从不重试：响应丢失后重试创建，会启动并计费
// 第二个任务。读取与删除保留 SDK 的重试。
package arkvideo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/volcengine/ark-runtime-go/arkruntime"
	"github.com/volcengine/ark-runtime-go/arkruntime/model/contentgeneration"
)

// DefaultBaseURL is Ark's Beijing endpoint, used when a credential names none.
//
// DefaultBaseURL 是方舟的北京地址，凭据没有指定地址时使用。
const DefaultBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

// Image roles Ark accepts on an image content item.
//
// 方舟在图片内容项上接受的角色。
const (
	RoleFirstFrame     = "first_frame"
	RoleLastFrame      = "last_frame"
	RoleReferenceImage = "reference_image"
)

// Status is a task's lifecycle state as Ark reports it. Values Ark adds later
// pass through unchanged.
//
// Status 是方舟报告的任务生命周期状态。方舟以后新增的取值原样透传。
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusExpired   Status = "expired"
)

// Terminal reports whether the task will not change state again.
//
// Terminal 报告任务是否不会再改变状态。
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusExpired:
		return true
	}
	return false
}

// Config names the key and endpoint a Client uses.
//
// Config 指定 Client 使用的 Key 与地址。
type Config struct {
	APIKey string
	// BaseURL is the API root, e.g. DefaultBaseURL. Empty means DefaultBaseURL.
	// BaseURL 是 API 根地址，例如 DefaultBaseURL。为空表示 DefaultBaseURL。
	BaseURL string
	// HTTPClient carries the API calls. Nil means the SDK's own client.
	// HTTPClient 承载 API 调用。为 nil 时使用 SDK 自己的客户端。
	HTTPClient *http.Client
}

// Client calls Ark's content generation task API with one key.
//
// Client 用一个 Key 调用方舟的内容生成任务 API。
type Client struct {
	create *arkruntime.Client
	other  *arkruntime.Client
}

// New returns a Client for cfg.
//
// New 按 cfg 返回一个 Client。
func New(cfg Config) *Client {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	opts := []arkruntime.ConfigOption{arkruntime.WithBaseUrl(base)}
	if cfg.HTTPClient != nil {
		opts = append(opts, arkruntime.WithHTTPClient(cfg.HTTPClient))
	}
	return &Client{
		create: arkruntime.NewClientWithApiKey(cfg.APIKey, append(opts, arkruntime.WithRetryTimes(0))...),
		other:  arkruntime.NewClientWithApiKey(cfg.APIKey, opts...),
	}
}

// Image is one image content item: a data: URL (or any URL Ark can fetch)
// and its role.
//
// Image 是一个图片内容项：一个 data: URL（或方舟能取到的任意 URL）及其角色。
type Image struct {
	Role string
	URL  string
}

// TaskRequest is a video generation task. Zero values leave the parameter to
// Ark's default.
//
// TaskRequest 是一个视频生成任务。零值表示该参数取方舟的默认值。
type TaskRequest struct {
	Model  string
	Prompt string
	Images []Image
	// Resolution such as "720p"; Ratio such as "16:9" or "adaptive".
	// Resolution 如 "720p"；Ratio 如 "16:9" 或 "adaptive"。
	Resolution string
	Ratio      string
	// Duration in seconds; 0 leaves it unset, -1 lets the model choose.
	// Duration 以秒计；0 表示不设置，-1 表示由模型决定。
	Duration        int64
	GenerateAudio   *bool
	Watermark       *bool
	ReturnLastFrame bool
	// ExpiresAfter bounds how long Ark keeps the task queued or running;
	// 0 leaves Ark's default. Ark rounds nothing: it must be whole seconds
	// within the range Ark accepts.
	// ExpiresAfter 限制方舟让任务排队或执行的时长；0 表示取方舟的默认值。必须是方舟接受
	// 范围内的整秒数。
	ExpiresAfter time.Duration
}

// Task is a snapshot of one task.
//
// Task 是一个任务的快照。
type Task struct {
	ID     string
	Model  string
	Status Status
	// VideoURL and LastFrameURL are set once the task succeeded. Ark keeps
	// them for about 24 hours.
	// VideoURL 与 LastFrameURL 在任务成功后才有值，方舟保留约 24 小时。
	VideoURL         string
	LastFrameURL     string
	CompletionTokens int64
	TotalTokens      int64
	// ErrorCode is Ark's error code for a failed task, kept only when it is
	// a plain identifier; Ark's message is never kept.
	// ErrorCode 是失败任务的方舟错误码，只在它是一个简单标识符时保留；方舟的消息从不保留。
	ErrorCode  string
	Resolution string
	Ratio      string
	Duration   int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListQuery filters one page of tasks. Zero values mean no filter and Ark's
// default page.
//
// ListQuery 筛选一页任务。零值表示不筛选并使用方舟的默认分页。
type ListQuery struct {
	PageNum  int
	PageSize int
	Status   Status
	Model    string
}

// TaskPage is one page of tasks and the total that match.
//
// TaskPage 是一页任务以及匹配的总数。
type TaskPage struct {
	Total int
	Items []Task
}

// Create starts a task and returns its id. It is never retried.
//
// Create 创建一个任务并返回其 id。从不重试。
func (c *Client) Create(ctx context.Context, req TaskRequest) (string, error) {
	body := &contentgeneration.CreateContentGenerationTaskRequest{Model: req.Model}
	if req.Prompt != "" {
		body.Content = append(body.Content, contentgeneration.ContentItem{
			Type: contentgeneration.ContentTypeText,
			Text: contentgeneration.NewOptString(req.Prompt),
		})
	}
	for _, img := range req.Images {
		item := contentgeneration.ContentItem{
			Type:     contentgeneration.ContentTypeImageURL,
			ImageURL: contentgeneration.NewOptImageURL(contentgeneration.ImageURL{URL: img.URL}),
		}
		if img.Role != "" {
			item.Role = contentgeneration.NewOptString(img.Role)
		}
		body.Content = append(body.Content, item)
	}
	if req.Resolution != "" {
		body.Resolution = contentgeneration.NewOptString(req.Resolution)
	}
	if req.Ratio != "" {
		body.Ratio = contentgeneration.NewOptString(req.Ratio)
	}
	if req.Duration != 0 {
		body.Duration = contentgeneration.NewOptInt64(req.Duration)
	}
	if req.GenerateAudio != nil {
		body.GenerateAudio = contentgeneration.NewOptBool(*req.GenerateAudio)
	}
	if req.Watermark != nil {
		body.Watermark = contentgeneration.NewOptBool(*req.Watermark)
	}
	if req.ReturnLastFrame {
		body.ReturnLastFrame = contentgeneration.NewOptBool(true)
	}
	if req.ExpiresAfter > 0 {
		body.ExecutionExpiresAfter = contentgeneration.NewOptInt64(int64(req.ExpiresAfter / time.Second))
	}
	resp, err := c.create.CreateContentGenerationTask(ctx, body)
	if err != nil {
		return "", classify(ctx, err)
	}
	if resp == nil || resp.ID == "" {
		return "", &Error{Kind: KindUpstream}
	}
	return resp.ID, nil
}

// Get returns the current snapshot of a task.
//
// Get 返回一个任务的当前快照。
func (c *Client) Get(ctx context.Context, id string) (Task, error) {
	if id == "" {
		return Task{}, &Error{Kind: KindInvalid}
	}
	resp, err := c.other.GetContentGenerationTask(ctx, id)
	if err != nil {
		return Task{}, classify(ctx, err)
	}
	return fromTask(resp.ContentGenerationTask), nil
}

// List returns one page of the key's tasks.
//
// List 返回该 Key 下任务的一页。
func (c *Client) List(ctx context.Context, q ListQuery) (TaskPage, error) {
	req := &arkruntime.ListContentGenerationTasksRequest{}
	if q.PageNum > 0 {
		v := int32(q.PageNum)
		req.PageNum = &v
	}
	if q.PageSize > 0 {
		v := int32(q.PageSize)
		req.PageSize = &v
	}
	if q.Status != "" {
		v := contentgeneration.TaskStatus(q.Status)
		req.Status = &v
	}
	if q.Model != "" {
		v := q.Model
		req.Model = &v
	}
	resp, err := c.other.ListContentGenerationTasks(ctx, req)
	if err != nil {
		return TaskPage{}, classify(ctx, err)
	}
	page := TaskPage{Total: int(resp.Total.Or(0))}
	items := resp.Items
	if len(items) == 0 {
		items = resp.Data
	}
	for _, t := range items {
		page.Items = append(page.Items, fromTask(t))
	}
	if page.Total < len(page.Items) {
		page.Total = len(page.Items)
	}
	return page, nil
}

// Delete cancels a queued task, or deletes the record of a finished one. Ark
// refuses it for a running task; that refusal is an *Error of KindConflict
// or KindInvalid depending on how Ark reports it.
//
// Delete 取消一个排队中的任务，或删除一个已结束任务的记录。对执行中的任务方舟会拒绝；
// 该拒绝是一个 *Error，按方舟的报告方式为 KindConflict 或 KindInvalid。
func (c *Client) Delete(ctx context.Context, id string) error {
	if id == "" {
		return &Error{Kind: KindInvalid}
	}
	if err := c.other.DeleteContentGenerationTask(ctx, id); err != nil {
		return classify(ctx, err)
	}
	return nil
}

// Probe checks that the key is accepted by listing one task.
//
// Probe 通过列出一个任务来检查 Key 是否被接受。
func (c *Client) Probe(ctx context.Context) error {
	_, err := c.List(ctx, ListQuery{PageNum: 1, PageSize: 1})
	return err
}

func fromTask(t contentgeneration.ContentGenerationTask) Task {
	out := Task{
		ID:         t.ID,
		Model:      t.Model,
		Status:     Status(t.Status),
		Resolution: t.Resolution.Or(""),
		Ratio:      t.Ratio.Or(""),
		Duration:   t.Duration.Or(0),
	}
	if content, ok := t.Content.Get(); ok {
		out.VideoURL = content.VideoURL.Or("")
		out.LastFrameURL = content.LastFrameURL.Or("")
	}
	if usage, ok := t.Usage.Get(); ok {
		out.CompletionTokens = usage.CompletionTokens
		out.TotalTokens = usage.TotalTokens
	}
	if e, ok := t.Error.Get(); ok {
		out.ErrorCode = safeCode(e.Code)
	}
	if v, ok := t.CreatedAt.Get(); ok && v > 0 {
		out.CreatedAt = time.Unix(v, 0).UTC()
	}
	if v, ok := t.UpdatedAt.Get(); ok && v > 0 {
		out.UpdatedAt = time.Unix(v, 0).UTC()
	}
	return out
}

// ContentRejected reports whether an Ark error code says the input or the
// output failed Ark's content review.
//
// ContentRejected 报告方舟错误码是否表示输入或输出未通过方舟的内容审核。
func ContentRejected(code string) bool {
	return strings.Contains(code, "Sensitive") || strings.Contains(code, "Risk")
}

// IsKind reports whether err is an *Error of kind k.
//
// IsKind 报告 err 是否为 k 类的 *Error。
func IsKind(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}
