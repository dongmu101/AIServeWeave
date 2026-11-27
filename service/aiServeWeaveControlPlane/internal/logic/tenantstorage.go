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

	"AIServeWeave/common/objectstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Limits on a tenant's storage fields. / 租户存储字段的上限。
const (
	MaxTenantStorageURLBytes    = 2048
	MaxTenantStorageRegionBytes = 64
	MaxTenantStorageBucketBytes = 255
	MaxTenantStoragePrefixBytes = 512
	MaxTenantStorageUserBytes   = 256
	MaxTenantStorageKeyIDBytes  = 256
	MaxTenantStorageSecretBytes = 1024
	MaxTenantStorageRetention   = 36500
	// TenantStorageProbeTimeout bounds the connectivity check a save runs.
	// TenantStorageProbeTimeout 限定保存时连通性检查的时长。
	TenantStorageProbeTimeout = 20 * time.Second
)

// Codes a refused tenant storage save carries. / 租户存储保存被拒时携带的错误码。
const (
	TenantStorageInvalid             = "invalid_storage"
	TenantStorageCredentialsRequired = "credentials_required"
	TenantStorageEndpointNotAllowed  = "endpoint_not_allowed"
	TenantStorageUnreachable         = "storage_unreachable"
)

// Reasons a storage_unreachable refusal carries. / storage_unreachable 携带的原因。
const (
	TenantStorageReasonNetwork            = "network"
	TenantStorageReasonAuth               = "auth"
	TenantStorageReasonNotFound           = "not_found"
	TenantStorageReasonPermission         = "permission"
	TenantStorageReasonEndpointNotAllowed = "endpoint_not_allowed"
	TenantStorageReasonOther              = "other"
)

// TenantStorageError is a refused save. It unwraps to ErrInvalidInput, so a
// caller that does not know it still answers 400.
//
// TenantStorageError 是一次被拒的保存。它 Unwrap 为 ErrInvalidInput，不认识它的调用方仍会
// 回答 400。
type TenantStorageError struct {
	Code   string
	Reason string
}

func (e *TenantStorageError) Error() string {
	if e.Reason != "" {
		return "tenant storage: " + e.Code + " (" + e.Reason + ")"
	}
	return "tenant storage: " + e.Code
}

func (e *TenantStorageError) Unwrap() error { return ErrInvalidInput }

// TenantStorageConfig enables tenants' own storage.
//
// TenantStorageConfig 开启租户自有存储。
type TenantStorageConfig struct {
	Secrets               *ProviderSecrets
	AllowPrivateEndpoints bool
	// Probe checks a candidate storage before it is saved. Nil builds it with
	// objectstore.NewTenant and runs objectstore.Probe.
	//
	// Probe 在保存前检查候选存储。为 nil 时用 objectstore.NewTenant 构建并运行
	// objectstore.Probe。
	Probe  func(ctx context.Context, cfg objectstore.Config, allowPrivate bool) error
	Logger *slog.Logger
}

// WithTenantStorage turns tenants' own storage on. Without it the tenant
// endpoints report it off and writes return ErrNotFound.
//
// WithTenantStorage 开启租户自有存储。没有它时租户接口报告未开启，写入返回 ErrNotFound。
func WithTenantStorage(cfg TenantStorageConfig) Option {
	if cfg.Probe == nil {
		cfg.Probe = probeTenantStorage
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return func(s *Service) { s.tenantStorage = &cfg }
}

func probeTenantStorage(ctx context.Context, cfg objectstore.Config, allowPrivate bool) error {
	backend, err := objectstore.NewTenant(cfg, allowPrivate)
	if err != nil {
		return err
	}
	return objectstore.Probe(ctx, backend)
}

// TenantStorageS3Input is the S3 part of a save. A nil key keeps the saved
// one, while the endpoint, region and bucket stay the same.
//
// TenantStorageS3Input 是保存中的 S3 部分。Key 为 nil 表示保留已保存的值，前提是 endpoint、
// region 与 bucket 不变。
type TenantStorageS3Input struct {
	Endpoint, Region, Bucket, Prefix string
	PathStyle                        bool
	AccessKeyID, SecretAccessKey     *string
}

// TenantStorageWebDAVInput is the WebDAV part of a save. A nil password keeps
// the saved one, while the URL and username stay the same.
//
// TenantStorageWebDAVInput 是保存中的 WebDAV 部分。Password 为 nil 表示保留已保存的值，前提是
// URL 与用户名不变。
type TenantStorageWebDAVInput struct {
	URL, Dir, Username string
	Password           *string
}

// TenantStorageInput is one save: Kind picks which of S3 and WebDAV is used,
// and the other must be nil.
//
// TenantStorageInput 是一次保存：Kind 选择使用 S3 还是 WebDAV，另一个必须为 nil。
type TenantStorageInput struct {
	Kind          string
	S3            *TenantStorageS3Input
	WebDAV        *TenantStorageWebDAVInput
	RetentionDays int
}

// TenantStorageS3View is the S3 part of a storage, without its secret.
// TenantStorageS3View 是存储的 S3 部分，不含密钥。
type TenantStorageS3View struct {
	Endpoint, Region, Bucket, Prefix string
	PathStyle                        bool
	AccessKeyIDLast4                 string
}

// TenantStorageWebDAVView is the WebDAV part of a storage, without its
// password.
// TenantStorageWebDAVView 是存储的 WebDAV 部分，不含密码。
type TenantStorageWebDAVView struct {
	URL, Dir, Username string
}

// TenantStorageView is a tenant's storage as its admins see it.
// TenantStorageView 是租户管理员看到的存储。
type TenantStorageView struct {
	Kind          string
	S3            *TenantStorageS3View
	WebDAV        *TenantStorageWebDAVView
	RetentionDays int
	UpdatedBy     string
	UpdatedAt     time.Time
}

// TenantStorageStatus answers the tenant read: whether the deployment allows
// tenants their own storage, and this tenant's, if any.
//
// TenantStorageStatus 是租户读取的答复：部署是否允许租户自有存储，以及本租户的存储（如有）。
type TenantStorageStatus struct {
	Enabled               bool
	AllowPrivateEndpoints bool
	Storage               *TenantStorageView
}

// TenantStorageSecret is a tenant's storage with its credentials, for the
// Gateway only.
//
// TenantStorageSecret 是带凭据的租户存储，只给 Gateway。
type TenantStorageSecret struct {
	TenantID      string
	Version       uint64
	Kind          string
	S3            *objectstore.S3Config
	WebDAV        *objectstore.WebDAVConfig
	RetentionDays int
}

// tenantStorageCredentials is the JSON sealed into SecretCiphertext.
// tenantStorageCredentials 是加密进 SecretCiphertext 的 JSON。
type tenantStorageCredentials struct {
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	Password        string `json:"password,omitempty"`
}

func tenantStorageAAD(tenantID string) []byte {
	return []byte("aisw-tenant-storage/v1\x00" + tenantID)
}

// authorizeTenantStorage requires a tenant owner or admin.
// authorizeTenantStorage 要求商户 owner 或 admin。
func authorizeTenantStorage(actor Actor) error {
	if actor.TenantID == "" || actor.UserID == "" || !actor.canManageKeys() {
		return ErrForbidden
	}
	return nil
}

// GetTenantStorage reports whether tenants may configure their own storage
// and the actor's tenant's storage, without credentials.
//
// GetTenantStorage 报告租户能否配置自有存储，以及 actor 所在租户的存储（不含凭据）。
func (s *Service) GetTenantStorage(ctx context.Context, actor Actor) (TenantStorageStatus, error) {
	if err := authorizeTenantStorage(actor); err != nil {
		return TenantStorageStatus{}, err
	}
	if s.tenantStorage == nil {
		return TenantStorageStatus{}, nil
	}
	out := TenantStorageStatus{Enabled: true, AllowPrivateEndpoints: s.tenantStorage.AllowPrivateEndpoints}
	row, err := s.store.GetTenantStorage(ctx, actor.TenantID)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return TenantStorageStatus{}, translate(err)
	}
	view := tenantStorageView(row)
	out.Storage = &view
	return out, nil
}

// PutTenantStorage validates in, checks that the storage works, and saves it
// for the actor's tenant.
//
// PutTenantStorage 校验 in、检查存储可用，然后为 actor 所在租户保存它。
func (s *Service) PutTenantStorage(ctx context.Context, actor Actor, in TenantStorageInput) (TenantStorageView, error) {
	if err := authorizeTenantStorage(actor); err != nil {
		return TenantStorageView{}, err
	}
	if s.tenantStorage == nil {
		return TenantStorageView{}, ErrNotFound
	}
	allowPrivate := s.tenantStorage.AllowPrivateEndpoints
	var existing *model.TenantStorage
	row, err := s.store.GetTenantStorage(ctx, actor.TenantID)
	switch {
	case err == nil:
		existing = &row
	case !errors.Is(err, store.ErrNotFound):
		return TenantStorageView{}, translate(err)
	}
	var saved tenantStorageCredentials
	if existing != nil {
		plain, err := s.tenantStorage.Secrets.openWith(tenantStorageAAD(actor.TenantID), existing.SecretCiphertext)
		if err == nil {
			err = json.Unmarshal([]byte(plain), &saved)
		}
		if err != nil {
			// The saved credentials no longer open (a changed master key):
			// keeping them is impossible, so the save must carry new ones.
			// 已保存的凭据打不开（主密钥变了）：无法保留，保存必须带上新凭据。
			existing = nil
		}
	}

	next, creds, cfg, verr := buildTenantStorage(actor.TenantID, in, existing, saved, allowPrivate)
	if verr != nil {
		return TenantStorageView{}, verr
	}
	probeCtx, cancel := context.WithTimeout(ctx, TenantStorageProbeTimeout)
	err = s.tenantStorage.Probe(probeCtx, cfg, allowPrivate)
	cancel()
	if err != nil {
		reason := tenantStorageReason(err)
		s.tenantStorage.Logger.Info("tenant storage probe failed / 租户存储连通性检查失败",
			slog.String("tenant_id", actor.TenantID), slog.String("reason", reason), slog.Any("error", err))
		return TenantStorageView{}, &TenantStorageError{Code: TenantStorageUnreachable, Reason: reason}
	}

	encoded, err := json.Marshal(creds)
	if err != nil {
		return TenantStorageView{}, err
	}
	ciphertext, err := s.tenantStorage.Secrets.sealWith(tenantStorageAAD(actor.TenantID), string(encoded))
	if err != nil {
		return TenantStorageView{}, ErrUnavailable
	}
	now := s.clock.Now().UTC()
	next.SecretCiphertext = ciphertext
	next.UpdatedBy = actor.UserID
	next.CreatedAt, next.UpdatedAt = now, now
	if err := s.store.PutTenantStorage(ctx, &next); err != nil {
		return TenantStorageView{}, translate(err)
	}
	detail := fmt.Sprintf("kind=%s host=%s", next.Kind, storageHost(next))
	if next.Kind == model.TenantStorageS3 {
		detail += " bucket=" + next.Bucket
	}
	detail += fmt.Sprintf(" retention_days=%d", next.RetentionDays)
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionTenantStorageUpdate, actor.TenantID, detail, actor.IP)
	return tenantStorageView(next), nil
}

// DeleteTenantStorage removes the actor's tenant's storage. Files already
// written stay where they are.
//
// DeleteTenantStorage 删除 actor 所在租户的存储。已写入的文件留在原处。
func (s *Service) DeleteTenantStorage(ctx context.Context, actor Actor) error {
	if err := authorizeTenantStorage(actor); err != nil {
		return err
	}
	if s.tenantStorage == nil {
		return ErrNotFound
	}
	if err := s.store.DeleteTenantStorage(ctx, actor.TenantID); err != nil {
		return translate(err)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionTenantStorageDelete, actor.TenantID, "", actor.IP)
	return nil
}

// TenantStorageEnabled reports whether tenants may configure their own
// storage, and whether private endpoints are allowed.
//
// TenantStorageEnabled 报告租户能否配置自有存储，以及是否允许内网端点。
func (s *Service) TenantStorageEnabled() (enabled, allowPrivate bool) {
	if s.tenantStorage == nil {
		return false, false
	}
	return true, s.tenantStorage.AllowPrivateEndpoints
}

// InternalTenantStorage returns tenantID's storage with its credentials for
// the Gateway. ok is false when the tenant has none or the feature is off.
// Credentials that do not open are ErrUnavailable, never "none": the Gateway
// must not fall back to the deployment's storage for a tenant that chose its
// own.
//
// InternalTenantStorage 为 Gateway 返回 tenantID 带凭据的存储。租户没有配置或功能未开启时
// ok 为 false。凭据打不开时返回 ErrUnavailable，而不是「没有」：对选择了自有存储的租户，
// Gateway 不能回退到部署存储。
func (s *Service) InternalTenantStorage(ctx context.Context, tenantID string) (TenantStorageSecret, bool, error) {
	if s.tenantStorage == nil || tenantID == "" {
		return TenantStorageSecret{}, false, nil
	}
	row, err := s.store.GetTenantStorage(ctx, tenantID)
	if errors.Is(err, store.ErrNotFound) {
		return TenantStorageSecret{}, false, nil
	}
	if err != nil {
		return TenantStorageSecret{}, false, translate(err)
	}
	secret, err := s.tenantStorageSecret(row)
	if err != nil {
		s.tenantStorage.Logger.Warn("tenant storage credentials do not open / 租户存储凭据无法解密",
			slog.String("tenant_id", tenantID), slog.Any("error", err))
		return TenantStorageSecret{}, false, ErrUnavailable
	}
	return secret, true, nil
}

// ListRetainedTenantStorage returns, for the Gateway's retention sweep, up to
// limit storages with a positive retention after the tenant id after, and the
// cursor for the next page ("" when there is none). A storage whose
// credentials do not open is skipped and logged.
//
// ListRetainedTenantStorage 为 Gateway 的保留期清理返回至多 limit 个保留天数为正、租户 id
// 排在 after 之后的存储，以及下一页的游标（没有下一页时为 ""）。凭据打不开的存储被跳过并
// 记录日志。
func (s *Service) ListRetainedTenantStorage(ctx context.Context, after string, limit int) ([]TenantStorageSecret, string, error) {
	if s.tenantStorage == nil {
		return []TenantStorageSecret{}, "", nil
	}
	if limit <= 0 || limit > store.MaxRetainedTenantStorage {
		limit = store.MaxRetainedTenantStorage
	}
	rows, err := s.store.ListRetainedTenantStorage(ctx, after, limit)
	if err != nil {
		return nil, "", translate(err)
	}
	out := make([]TenantStorageSecret, 0, len(rows))
	for _, row := range rows {
		secret, err := s.tenantStorageSecret(row)
		if err != nil {
			s.tenantStorage.Logger.Warn("tenant storage credentials do not open / 租户存储凭据无法解密",
				slog.String("tenant_id", row.TenantID), slog.Any("error", err))
			continue
		}
		out = append(out, secret)
	}
	next := ""
	if len(rows) == limit {
		next = rows[len(rows)-1].TenantID
	}
	return out, next, nil
}

func (s *Service) tenantStorageSecret(row model.TenantStorage) (TenantStorageSecret, error) {
	plain, err := s.tenantStorage.Secrets.openWith(tenantStorageAAD(row.TenantID), row.SecretCiphertext)
	if err != nil {
		return TenantStorageSecret{}, err
	}
	var creds tenantStorageCredentials
	if err := json.Unmarshal([]byte(plain), &creds); err != nil {
		return TenantStorageSecret{}, errors.New("credentials are not valid JSON")
	}
	out := TenantStorageSecret{TenantID: row.TenantID, Version: row.Version, Kind: row.Kind, RetentionDays: row.RetentionDays}
	cfg := storageConfig(row, creds)
	switch row.Kind {
	case model.TenantStorageS3:
		out.S3 = &cfg.S3
	case model.TenantStorageWebDAV:
		out.WebDAV = &cfg.WebDAV
	default:
		return TenantStorageSecret{}, fmt.Errorf("unknown kind %q", row.Kind)
	}
	return out, nil
}

// buildTenantStorage validates in against the saved row (nil when there is
// none, or its credentials do not open) and returns the row to save, the
// credentials to seal and the backend config to probe.
//
// buildTenantStorage 对照已保存的行（没有、或凭据打不开时为 nil）校验 in，返回要保存的行、
// 要加密的凭据，以及要检查的后端配置。
func buildTenantStorage(tenantID string, in TenantStorageInput, existing *model.TenantStorage, saved tenantStorageCredentials, allowPrivate bool) (model.TenantStorage, tenantStorageCredentials, objectstore.Config, error) {
	invalid := &TenantStorageError{Code: TenantStorageInvalid}
	none := model.TenantStorage{}
	if in.RetentionDays < 0 || in.RetentionDays > MaxTenantStorageRetention {
		return none, tenantStorageCredentials{}, objectstore.Config{}, invalid
	}
	row := model.TenantStorage{TenantID: tenantID, Kind: in.Kind, RetentionDays: in.RetentionDays}
	var creds tenantStorageCredentials
	switch in.Kind {
	case model.TenantStorageS3:
		if in.S3 == nil || in.WebDAV != nil {
			return none, creds, objectstore.Config{}, invalid
		}
		c := in.S3
		prefix, ok := cleanStoragePath(c.Prefix)
		if !ok || c.Bucket == "" || strings.Contains(c.Bucket, "/") ||
			!fieldOK(c.Endpoint, MaxTenantStorageURLBytes) || !fieldOK(c.Region, MaxTenantStorageRegionBytes) ||
			!fieldOK(c.Bucket, MaxTenantStorageBucketBytes) || !fieldOK(prefix, MaxTenantStoragePrefixBytes) {
			return none, creds, objectstore.Config{}, invalid
		}
		if c.Endpoint != "" {
			if err := endpointError(c.Endpoint, allowPrivate); err != nil {
				return none, creds, objectstore.Config{}, err
			}
		}
		row.Endpoint, row.Region, row.Bucket, row.Prefix, row.PathStyle = c.Endpoint, c.Region, c.Bucket, prefix, c.PathStyle
		// A key left out keeps the saved one, which only the same location
		// may do: saved keys never follow the storage to another server.
		// 省略的 Key 保留已保存的值，且只在位置不变时允许：已保存的 Key 从不跟随存储换到
		// 另一台服务器。
		if c.AccessKeyID == nil || c.SecretAccessKey == nil {
			if existing == nil || existing.Kind != model.TenantStorageS3 || existing.Endpoint != row.Endpoint ||
				existing.Region != row.Region || existing.Bucket != row.Bucket || saved.AccessKeyID == "" {
				return none, creds, objectstore.Config{}, &TenantStorageError{Code: TenantStorageCredentialsRequired}
			}
		}
		creds = tenantStorageCredentials{AccessKeyID: saved.AccessKeyID, SecretAccessKey: saved.SecretAccessKey}
		if c.AccessKeyID != nil {
			if *c.AccessKeyID == "" || !fieldOK(*c.AccessKeyID, MaxTenantStorageKeyIDBytes) {
				return none, creds, objectstore.Config{}, invalid
			}
			creds.AccessKeyID = *c.AccessKeyID
		}
		if c.SecretAccessKey != nil {
			if *c.SecretAccessKey == "" || !fieldOK(*c.SecretAccessKey, MaxTenantStorageSecretBytes) {
				return none, creds, objectstore.Config{}, invalid
			}
			creds.SecretAccessKey = *c.SecretAccessKey
		}
		row.SecretHint = last4(creds.AccessKeyID)
	case model.TenantStorageWebDAV:
		if in.WebDAV == nil || in.S3 != nil {
			return none, creds, objectstore.Config{}, invalid
		}
		c := in.WebDAV
		dir, ok := cleanStoragePath(c.Dir)
		if !ok || c.URL == "" || !fieldOK(c.URL, MaxTenantStorageURLBytes) ||
			!fieldOK(dir, MaxTenantStoragePrefixBytes) || !fieldOK(c.Username, MaxTenantStorageUserBytes) {
			return none, creds, objectstore.Config{}, invalid
		}
		if err := endpointError(c.URL, allowPrivate); err != nil {
			return none, creds, objectstore.Config{}, err
		}
		row.Endpoint, row.Prefix, row.Username = c.URL, dir, c.Username
		if c.Password != nil {
			if !fieldOK(*c.Password, MaxTenantStorageSecretBytes) {
				return none, creds, objectstore.Config{}, invalid
			}
			creds = tenantStorageCredentials{Password: *c.Password}
		} else {
			if existing == nil || existing.Kind != model.TenantStorageWebDAV || existing.Endpoint != row.Endpoint || existing.Username != row.Username {
				return none, creds, objectstore.Config{}, &TenantStorageError{Code: TenantStorageCredentialsRequired}
			}
			creds = tenantStorageCredentials{Password: saved.Password}
		}
	default:
		return none, creds, objectstore.Config{}, invalid
	}
	return row, creds, storageConfig(row, creds), nil
}

// storageConfig is the backend config for row with creds.
// storageConfig 是 row 加上 creds 的后端配置。
func storageConfig(row model.TenantStorage, creds tenantStorageCredentials) objectstore.Config {
	cfg := objectstore.Config{Kind: row.Kind}
	switch row.Kind {
	case model.TenantStorageS3:
		cfg.S3 = objectstore.S3Config{
			Endpoint: row.Endpoint, Region: row.Region, Bucket: row.Bucket, Prefix: row.Prefix, UsePathStyle: row.PathStyle,
			AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey,
		}
	case model.TenantStorageWebDAV:
		cfg.WebDAV = objectstore.WebDAVConfig{URL: row.Endpoint, Dir: row.Prefix, Username: row.Username, Password: creds.Password}
	}
	return cfg
}

func tenantStorageView(row model.TenantStorage) TenantStorageView {
	view := TenantStorageView{Kind: row.Kind, RetentionDays: row.RetentionDays, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt}
	switch row.Kind {
	case model.TenantStorageS3:
		view.S3 = &TenantStorageS3View{Endpoint: row.Endpoint, Region: row.Region, Bucket: row.Bucket, Prefix: row.Prefix,
			PathStyle: row.PathStyle, AccessKeyIDLast4: row.SecretHint}
	case model.TenantStorageWebDAV:
		view.WebDAV = &TenantStorageWebDAVView{URL: row.Endpoint, Dir: row.Prefix, Username: row.Username}
	}
	return view
}

// endpointError maps objectstore.CheckEndpoint onto a refusal.
// endpointError 把 objectstore.CheckEndpoint 的结果映射为拒绝。
func endpointError(raw string, allowPrivate bool) error {
	err := objectstore.CheckEndpoint(raw, allowPrivate)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, objectstore.ErrAddressNotAllowed):
		return &TenantStorageError{Code: TenantStorageEndpointNotAllowed}
	default:
		return &TenantStorageError{Code: TenantStorageInvalid}
	}
}

// tenantStorageReason classifies a failed probe without quoting the server.
// tenantStorageReason 对失败的连通性检查分类，不引用服务端原文。
func tenantStorageReason(err error) string {
	switch {
	case errors.Is(err, objectstore.ErrAddressNotAllowed):
		return TenantStorageReasonEndpointNotAllowed
	case errors.Is(err, objectstore.ErrAuth):
		return TenantStorageReasonAuth
	case errors.Is(err, objectstore.ErrPermission):
		return TenantStorageReasonPermission
	case errors.Is(err, objectstore.ErrContainerNotFound):
		return TenantStorageReasonNotFound
	case errors.Is(err, objectstore.ErrUnreachable), errors.Is(err, context.DeadlineExceeded):
		return TenantStorageReasonNetwork
	default:
		return TenantStorageReasonOther
	}
}

// cleanStoragePath trims surrounding slashes from a prefix or directory and
// rejects one with empty, "." or ".." segments or a backslash.
//
// cleanStoragePath 去掉前缀或目录两端的斜杠，拒绝含空段、"."、".." 段或反斜杠的值。
func cleanStoragePath(p string) (string, bool) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", true
	}
	if strings.Contains(p, `\`) {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return p, true
}

// fieldOK bounds a field and rejects control characters in it.
// fieldOK 限定字段长度，并拒绝其中的控制字符。
func fieldOK(v string, max int) bool {
	if len(v) > max {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// storageHost is the audited host of a storage, "aws" for S3 without an
// endpoint.
// storageHost 是审计记录的存储主机名；没有端点的 S3 记为 "aws"。
func storageHost(row model.TenantStorage) string {
	if row.Endpoint == "" {
		return "aws"
	}
	if u, err := url.Parse(row.Endpoint); err == nil {
		return u.Hostname()
	}
	return ""
}
