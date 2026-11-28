package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"AIServeWeave/common/arkvideo"
	"AIServeWeave/common/flowdef"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Limits on a saved Ark credential. / 已保存方舟凭据的上限。
const (
	MaxArkKeyBytes     = 512
	MaxArkBaseURLBytes = 2048
	MaxArkModels       = 100
	MaxArkTaskIDBytes  = 128
	MaxArkTaskPage     = 100
	// ArkProbeTimeout bounds the key check a save runs, and ArkCallTimeout
	// one task list or cancel call.
	// ArkProbeTimeout 限定保存时检查 Key 的时长，ArkCallTimeout 限定一次任务列表或取消调用。
	ArkProbeTimeout = 20 * time.Second
	ArkCallTimeout  = 20 * time.Second
)

// Ark credential sources the Gateway is told. / 告知 Gateway 的方舟凭据来源。
const (
	ArkSourceTenant   = "tenant"
	ArkSourcePlatform = "platform"
)

// Codes a refused Ark save or task call carries. / 方舟保存或任务调用被拒时携带的错误码。
const (
	ArkInvalid           = "invalid_ark"
	ArkKeyRequired       = "key_required"
	ArkUnreachable       = "ark_unreachable"
	ArkTaskNotCancelable = "task_not_cancelable"
)

// Reasons an ark_unreachable refusal carries. / ark_unreachable 携带的原因。
const (
	ArkReasonNetwork     = "network"
	ArkReasonAuth        = "auth"
	ArkReasonRateLimited = "rate_limited"
	ArkReasonOther       = "other"
)

// ArkError is a refused Ark save or task call. ark_unreachable unwraps to
// ErrUnavailable and every other code to ErrInvalidInput, so a caller that
// does not know it still answers 503 or 400.
//
// ArkError 是一次被拒的方舟保存或任务调用。ark_unreachable Unwrap 为 ErrUnavailable，其余
// 错误码 Unwrap 为 ErrInvalidInput，不认识它的调用方仍会回答 503 或 400。
type ArkError struct {
	Code   string
	Reason string
}

func (e *ArkError) Error() string {
	if e.Reason != "" {
		return "ark: " + e.Code + " (" + e.Reason + ")"
	}
	return "ark: " + e.Code
}

func (e *ArkError) Unwrap() error {
	if e.Code == ArkUnreachable {
		return ErrUnavailable
	}
	return ErrInvalidInput
}

// ArkClient is what the control plane asks of Ark with a saved key.
//
// ArkClient 是控制面用已保存的 Key 向方舟发起的调用。
type ArkClient interface {
	Probe(ctx context.Context) error
	List(ctx context.Context, q arkvideo.ListQuery) (arkvideo.TaskPage, error)
	Delete(ctx context.Context, id string) error
}

// ArkConfig enables saved Ark keys.
//
// ArkConfig 开启已保存的方舟 Key。
type ArkConfig struct {
	Secrets *ProviderSecrets
	// NewClient builds a client for one key. Nil uses arkvideo.New.
	// NewClient 为一个 Key 构建客户端。为 nil 时使用 arkvideo.New。
	NewClient func(cfg arkvideo.Config) ArkClient
	Logger    *slog.Logger
}

// WithArk turns saved Ark keys on. Without it the endpoints report the
// feature off and writes return ErrNotFound.
//
// WithArk 开启已保存的方舟 Key。没有它时接口报告未开启，写入返回 ErrNotFound。
func WithArk(cfg ArkConfig) Option {
	if cfg.NewClient == nil {
		cfg.NewClient = func(c arkvideo.Config) ArkClient { return arkvideo.New(c) }
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return func(s *Service) { s.ark = &cfg }
}

// ArkInput is one save. A nil APIKey keeps the saved key, provided BaseURL
// stays the same. BaseURL may only be set on the platform key.
//
// ArkInput 是一次保存。APIKey 为 nil 表示保留已保存的 Key，前提是 BaseURL 不变。BaseURL
// 只能在平台 Key 上设置。
type ArkInput struct {
	APIKey  *string
	BaseURL string
	Models  []string
}

// ArkView is a saved key as its owner sees it, without the key.
//
// ArkView 是保存者看到的已保存 Key，不含 Key 本身。
type ArkView struct {
	KeyLast4  string
	BaseURL   string
	Models    []string
	UpdatedBy string
	UpdatedAt time.Time
}

// ArkStatus answers a read: whether the deployment allows saving Ark keys,
// the actor's scope's key, if any, and for a tenant whether the platform
// key would be used in its place.
//
// ArkStatus 是读取的答复：部署是否允许保存方舟 Key、actor 所在 scope 的 Key（如有），以及
// 对租户而言，平台 Key 是否会代替它使用。
type ArkStatus struct {
	Enabled           bool
	Credential        *ArkView
	PlatformAvailable bool
}

// ArkSecret is the key a tenant's video node uses, for the Gateway only.
// Source says whose it is: the tenant's own, or the platform's.
//
// ArkSecret 是租户视频节点使用的 Key，只给 Gateway。Source 说明它属于谁：租户自己的，或
// 平台的。
type ArkSecret struct {
	Source  string
	Version uint64
	APIKey  string
	BaseURL string
	Models  []string
}

func arkAAD(scope string) []byte {
	return []byte("aisw-ark-credential/v1\x00" + scope)
}

// arkScope is the scope an actor reads and writes: the platform's for a
// platform operator, its own tenant's for a tenant owner or admin.
//
// arkScope 是 actor 读写的 scope：平台运维为平台的，租户 owner 或 admin 为其所在租户的。
func arkScope(actor Actor) (string, error) {
	if isPlatformActor(actor) {
		return model.PlatformScope, nil
	}
	if err := authorizeTenantStorage(actor); err != nil {
		return "", err
	}
	return actor.TenantID, nil
}

// GetArk reports whether Ark keys may be saved and the actor's scope's key,
// without the key itself.
//
// GetArk 报告能否保存方舟 Key，以及 actor 所在 scope 的 Key（不含 Key 本身）。
func (s *Service) GetArk(ctx context.Context, actor Actor) (ArkStatus, error) {
	scope, err := arkScope(actor)
	if err != nil {
		return ArkStatus{}, err
	}
	if s.ark == nil {
		return ArkStatus{}, nil
	}
	out := ArkStatus{Enabled: true}
	row, err := s.store.GetArkCredential(ctx, scope)
	switch {
	case err == nil:
		view := arkView(row)
		out.Credential = &view
	case !errors.Is(err, store.ErrNotFound):
		return ArkStatus{}, translate(err)
	}
	if scope != model.PlatformScope {
		_, err := s.store.GetArkCredential(ctx, model.PlatformScope)
		switch {
		case err == nil:
			out.PlatformAvailable = true
		case !errors.Is(err, store.ErrNotFound):
			return ArkStatus{}, translate(err)
		}
	}
	return out, nil
}

// PutArk validates in, checks the key with Ark, and saves it for the
// actor's scope.
//
// PutArk 校验 in、向方舟检查 Key，然后为 actor 所在 scope 保存它。
func (s *Service) PutArk(ctx context.Context, actor Actor, in ArkInput) (ArkView, error) {
	scope, err := arkScope(actor)
	if err != nil {
		return ArkView{}, err
	}
	if s.ark == nil {
		return ArkView{}, ErrNotFound
	}
	baseURL, verr := checkArkBaseURL(scope, in.BaseURL)
	if verr != nil {
		return ArkView{}, verr
	}
	models, verr := checkArkModels(in.Models)
	if verr != nil {
		return ArkView{}, verr
	}

	var key string
	switch {
	case in.APIKey != nil:
		key = strings.TrimSpace(*in.APIKey)
		if key == "" || len(key) > MaxArkKeyBytes || !printableASCII(key) {
			return ArkView{}, &ArkError{Code: ArkInvalid}
		}
	default:
		// Keeping the key is allowed only while it would go to the same
		// place: otherwise a save could send the saved key to a new host.
		// 只有 Key 仍发往同一地址时才允许保留：否则一次保存就能把已保存的 Key 发往新的主机。
		row, err := s.store.GetArkCredential(ctx, scope)
		if errors.Is(err, store.ErrNotFound) {
			return ArkView{}, &ArkError{Code: ArkKeyRequired}
		}
		if err != nil {
			return ArkView{}, translate(err)
		}
		if row.BaseURL != baseURL {
			return ArkView{}, &ArkError{Code: ArkKeyRequired}
		}
		plain, err := s.ark.Secrets.openWith(arkAAD(scope), row.KeyCiphertext)
		if err != nil {
			// A changed master key: the saved key cannot be kept.
			// 主密钥变了：已保存的 Key 无法保留。
			return ArkView{}, &ArkError{Code: ArkKeyRequired}
		}
		key = plain
	}

	probeCtx, cancel := context.WithTimeout(ctx, ArkProbeTimeout)
	err = s.ark.NewClient(arkvideo.Config{APIKey: key, BaseURL: baseURL}).Probe(probeCtx)
	cancel()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ArkView{}, ctxErr
		}
		reason := arkReason(err)
		s.ark.Logger.Info("ark key check failed / 方舟 Key 检查失败",
			slog.String("scope", scope), slog.String("reason", reason), slog.Any("error", err))
		return ArkView{}, &ArkError{Code: ArkUnreachable, Reason: reason}
	}

	ciphertext, err := s.ark.Secrets.sealWith(arkAAD(scope), key)
	if err != nil {
		return ArkView{}, ErrUnavailable
	}
	encodedModels, err := json.Marshal(models)
	if err != nil {
		return ArkView{}, err
	}
	now := s.clock.Now().UTC()
	row := model.ArkCredential{
		Scope: scope, KeyCiphertext: ciphertext, KeyLast4: last4(key), BaseURL: baseURL,
		Models: string(encodedModels), UpdatedBy: actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.PutArkCredential(ctx, &row); err != nil {
		return ArkView{}, translate(err)
	}
	s.audit(ctx, scope, actor.UserID, model.ActionArkCredentialUpdate, scope,
		fmt.Sprintf("host=%s key_last4=%s models=%d", arkHost(baseURL), row.KeyLast4, len(models)), actor.IP)
	return arkView(row), nil
}

// DeleteArk removes the actor's scope's key. A tenant without its own key
// falls back to the platform's, if any.
//
// DeleteArk 删除 actor 所在 scope 的 Key。没有自己 Key 的租户回退到平台 Key（如有）。
func (s *Service) DeleteArk(ctx context.Context, actor Actor) error {
	scope, err := arkScope(actor)
	if err != nil {
		return err
	}
	if s.ark == nil {
		return ErrNotFound
	}
	if err := s.store.DeleteArkCredential(ctx, scope); err != nil {
		return translate(err)
	}
	s.audit(ctx, scope, actor.UserID, model.ActionArkCredentialDelete, scope, "", actor.IP)
	return nil
}

// ListArkTasks returns one page of the tasks under the actor's scope's own
// key. A tenant without its own key gets ErrNotFound: the platform key's
// tasks belong to every tenant and are listed only to the operator.
//
// ListArkTasks 返回 actor 所在 scope 自己 Key 下任务的一页。没有自己 Key 的租户得到
// ErrNotFound：平台 Key 下的任务属于所有租户，只列给运维。
func (s *Service) ListArkTasks(ctx context.Context, actor Actor, q arkvideo.ListQuery) (arkvideo.TaskPage, error) {
	if q.PageNum < 0 || q.PageSize < 0 || q.PageSize > MaxArkTaskPage || !validArkStatus(q.Status) ||
		len(q.Model) > flowdef.MaxModelLength || !printableASCII(q.Model) {
		return arkvideo.TaskPage{}, ErrInvalidInput
	}
	client, scope, err := s.arkClient(ctx, actor)
	if err != nil {
		return arkvideo.TaskPage{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, ArkCallTimeout)
	defer cancel()
	page, err := client.List(callCtx, q)
	if err != nil {
		return arkvideo.TaskPage{}, s.arkCallError(ctx, scope, "list", err)
	}
	return page, nil
}

// DeleteArkTask cancels a queued task under the actor's scope's own key, or
// deletes the record of a finished one. Ark refuses it for a running task.
//
// DeleteArkTask 取消 actor 所在 scope 自己 Key 下排队中的任务，或删除已结束任务的记录。
// 对执行中的任务方舟会拒绝。
func (s *Service) DeleteArkTask(ctx context.Context, actor Actor, taskID string) error {
	if taskID == "" || len(taskID) > MaxArkTaskIDBytes || !validArkTaskID(taskID) {
		return ErrInvalidInput
	}
	client, scope, err := s.arkClient(ctx, actor)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, ArkCallTimeout)
	defer cancel()
	if err := client.Delete(callCtx, taskID); err != nil {
		return s.arkCallError(ctx, scope, "delete", err)
	}
	s.audit(ctx, scope, actor.UserID, model.ActionArkTaskDelete, taskID, "", actor.IP)
	return nil
}

// arkClient builds a client with the actor's scope's own saved key.
func (s *Service) arkClient(ctx context.Context, actor Actor) (ArkClient, string, error) {
	scope, err := arkScope(actor)
	if err != nil {
		return nil, "", err
	}
	if s.ark == nil {
		return nil, "", ErrNotFound
	}
	row, err := s.store.GetArkCredential(ctx, scope)
	if err != nil {
		return nil, "", translate(err)
	}
	key, err := s.ark.Secrets.openWith(arkAAD(scope), row.KeyCiphertext)
	if err != nil {
		s.ark.Logger.Warn("ark key does not open / 方舟 Key 无法解密", slog.String("scope", scope), slog.Any("error", err))
		return nil, "", ErrUnavailable
	}
	return s.ark.NewClient(arkvideo.Config{APIKey: key, BaseURL: row.BaseURL}), scope, nil
}

func (s *Service) arkCallError(ctx context.Context, scope, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case arkvideo.IsKind(err, arkvideo.KindNotFound):
		return ErrNotFound
	case arkvideo.IsKind(err, arkvideo.KindConflict), arkvideo.IsKind(err, arkvideo.KindInvalid):
		if op == "delete" {
			return &ArkError{Code: ArkTaskNotCancelable}
		}
		return ErrInvalidInput
	}
	reason := arkReason(err)
	s.ark.Logger.Info("ark call failed / 方舟调用失败",
		slog.String("scope", scope), slog.String("op", op), slog.String("reason", reason), slog.Any("error", err))
	return &ArkError{Code: ArkUnreachable, Reason: reason}
}

// InternalArk returns the key tenantID's video node uses, for the Gateway:
// the tenant's own when it saved one, otherwise the platform's. ok is false
// when there is neither or the feature is off. A key that does not open is
// ErrUnavailable, never a fall back to the other: a tenant that saved its
// own key must not be billed on the platform's because of a changed master
// key.
//
// InternalArk 为 Gateway 返回 tenantID 的视频节点使用的 Key：租户保存了自己的就用它，否则用
// 平台的。两者都没有或功能未开启时 ok 为 false。Key 打不开时返回 ErrUnavailable，从不回退到
// 另一个：保存了自己 Key 的租户，不能因为主密钥变了就改用平台 Key 计费。
func (s *Service) InternalArk(ctx context.Context, tenantID string) (ArkSecret, bool, error) {
	if s.ark == nil || tenantID == "" || tenantID == model.PlatformScope {
		return ArkSecret{}, false, nil
	}
	source := ArkSourceTenant
	row, err := s.store.GetArkCredential(ctx, tenantID)
	if errors.Is(err, store.ErrNotFound) {
		source = ArkSourcePlatform
		row, err = s.store.GetArkCredential(ctx, model.PlatformScope)
	}
	if errors.Is(err, store.ErrNotFound) {
		return ArkSecret{}, false, nil
	}
	if err != nil {
		return ArkSecret{}, false, translate(err)
	}
	key, err := s.ark.Secrets.openWith(arkAAD(row.Scope), row.KeyCiphertext)
	if err != nil {
		s.ark.Logger.Warn("ark key does not open / 方舟 Key 无法解密", slog.String("scope", row.Scope), slog.Any("error", err))
		return ArkSecret{}, false, ErrUnavailable
	}
	return ArkSecret{Source: source, Version: row.Version, APIKey: key, BaseURL: row.BaseURL, Models: arkModels(row.Models)}, true, nil
}

// PlatformArkModels returns the models the platform key allows, for the
// unpriced-alias report. It is empty when there is no platform key.
//
// PlatformArkModels 返回平台 Key 允许的模型，供未定价别名报告使用。没有平台 Key 时为空。
func (s *Service) PlatformArkModels(ctx context.Context) ([]string, error) {
	if s.ark == nil {
		return nil, nil
	}
	row, err := s.store.GetArkCredential(ctx, model.PlatformScope)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, translate(err)
	}
	return arkModels(row.Models), nil
}

func arkView(row model.ArkCredential) ArkView {
	return ArkView{KeyLast4: row.KeyLast4, BaseURL: row.BaseURL, Models: arkModels(row.Models), UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt}
}

func arkModels(encoded string) []string {
	models := []string{}
	if encoded == "" || json.Unmarshal([]byte(encoded), &models) != nil {
		return []string{}
	}
	return models
}

// checkArkBaseURL normalises a base URL. Only the platform key may name one:
// a tenant's key always goes to Ark's own endpoint, so a tenant cannot point
// the platform at an address of its choosing.
//
// checkArkBaseURL 规范化 base URL。只有平台 Key 可以指定：租户的 Key 总是发往方舟自己的
// 地址，租户因此无法让平台连接它选定的地址。
func checkArkBaseURL(scope, raw string) (string, *ArkError) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" || raw == arkvideo.DefaultBaseURL {
		return "", nil
	}
	if scope != model.PlatformScope {
		return "", &ArkError{Code: ArkInvalid}
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > MaxArkBaseURLBytes || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", &ArkError{Code: ArkInvalid}
	}
	return raw, nil
}

// checkArkModels trims, de-duplicates and checks model ids: each must be a
// model name a video_generation node could hold.
//
// checkArkModels 去除空白、去重并检查模型 id：每个都必须是 video_generation 节点能保存的模型名。
func checkArkModels(in []string) ([]string, *ArkError) {
	if len(in) > MaxArkModels {
		return nil, &ArkError{Code: ArkInvalid}
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || len(m) > flowdef.MaxModelLength || !utf8.ValidString(m) || strings.Contains(m, "{{") ||
			strings.IndexFunc(m, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return nil, &ArkError{Code: ArkInvalid}
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, nil
}

func arkReason(err error) string {
	switch {
	case arkvideo.IsKind(err, arkvideo.KindAuth):
		return ArkReasonAuth
	case arkvideo.IsKind(err, arkvideo.KindNetwork), errors.Is(err, context.DeadlineExceeded):
		return ArkReasonNetwork
	case arkvideo.IsKind(err, arkvideo.KindRateLimited):
		return ArkReasonRateLimited
	}
	return ArkReasonOther
}

func arkHost(baseURL string) string {
	if baseURL == "" {
		return "default"
	}
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "custom"
}

func validArkStatus(s arkvideo.Status) bool {
	switch s {
	case "", arkvideo.StatusQueued, arkvideo.StatusRunning, arkvideo.StatusSucceeded,
		arkvideo.StatusFailed, arkvideo.StatusCancelled, arkvideo.StatusExpired:
		return true
	}
	return false
}

func validArkTaskID(id string) bool {
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
