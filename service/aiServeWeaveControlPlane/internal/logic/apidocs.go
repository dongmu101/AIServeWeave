// apidocs.go implements the tenant-facing API reference: tenants read
// published pages, platform operators create, edit, hide and delete them,
// and SyncAPIDocSeeds keeps compiled-in pages current at startup.
//
// apidocs.go 实现面向租户的 API 参考文档：租户读取已发布页面，平台运维创建、编辑、
// 隐藏与删除页面，SyncAPIDocSeeds 在启动时让编译进来的页面保持最新。
package logic

import (
	"context"
	"errors"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/apidocs"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// APIDocSummary is one page's navigation entry, as tenants see it.
//
// APIDocSummary 是一个页面在导航中的条目，即租户看到的样子。
type APIDocSummary struct {
	Slug string `json:"slug"`
	apidocs.Meta
	UpdatedAt time.Time `json:"updated_at"`
}

// APIDocDetail is one whole page, as tenants see it.
//
// APIDocDetail 是完整的一个页面，即租户看到的样子。
type APIDocDetail struct {
	APIDocSummary
	Content apidocs.Content `json:"content"`
}

// OperatorAPIDocSummary adds the management fields tenants never see.
//
// OperatorAPIDocSummary 在租户视图之上增加租户永远看不到的管理字段。
type OperatorAPIDocSummary struct {
	APIDocSummary
	Status       string `json:"status"`
	Source       string `json:"source"`
	SeedRevision int64  `json:"seed_revision"`
	// Version is the CAS token every write must echo. / Version 是每次写入都必须回传的 CAS 令牌。
	Version   int64  `json:"version"`
	UpdatedBy string `json:"updated_by,omitempty"`
	// Seeded reports that a compiled-in seed exists for this slug: the page
	// can be reset to it, and cannot be deleted because the next start would
	// recreate it. / Seeded 表示该 slug 存在编译进来的种子：页面可以重置为种子，
	// 但不能删除，因为下次启动会把它重新建出来。
	Seeded bool `json:"seeded"`
}

// OperatorAPIDocDetail is one whole page with its management fields.
//
// OperatorAPIDocDetail 是带管理字段的完整页面。
type OperatorAPIDocDetail struct {
	OperatorAPIDocSummary
	Content apidocs.Content `json:"content"`
}

// APIDocInput is what an operator writes: everything except the slug, which
// is the resource path, and the management fields the service owns.
//
// APIDocInput 是运维写入的内容：除 slug（它是资源路径）与服务自己维护的管理字段
// 以外的一切。
type APIDocInput struct {
	apidocs.Meta
	Status  string          `json:"status"`
	Content apidocs.Content `json:"content"`
}

// SyncAPIDocSeeds inserts every compiled-in page that is missing and
// refreshes every page still holding an older revision of its untouched
// seed. It returns how many rows changed. Pages an operator has written are
// never touched.
//
// SyncAPIDocSeeds 插入所有缺失的编译内置页面，并刷新所有仍持有其未改动种子旧
// revision 的页面。返回变化的行数。运维写过的页面永远不会被触碰。
func (s *Service) SyncAPIDocSeeds(ctx context.Context) (int, error) {
	seeds, err := apidocs.Seeds()
	if err != nil {
		return 0, err
	}
	now := s.clock.Now().UTC()
	changed := 0
	for _, seed := range seeds {
		encoded, err := apidocs.Validate(seed.Meta, seed.Content)
		if err != nil {
			return changed, err
		}
		row := apiDocRow(seed.Slug, seed.Meta, encoded)
		row.Status, row.Source, row.SeedRevision, row.Version, row.UpdatedAt = apidocs.StatusPublished, apidocs.SourceSeed, seed.Revision, 1, now
		ok, err := s.store.SeedAPIDoc(ctx, &row)
		if err != nil {
			return changed, translate(err)
		}
		if ok {
			changed++
		}
	}
	return changed, nil
}

// ListAPIDocs returns every published page's navigation entry.
//
// ListAPIDocs 返回所有已发布页面的导航条目。
func (s *Service) ListAPIDocs(ctx context.Context) ([]APIDocSummary, error) {
	rows, err := s.store.ListAPIDocs(ctx, true)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]APIDocSummary, len(rows))
	for i, row := range rows {
		out[i] = apiDocSummary(row)
	}
	return out, nil
}

// GetAPIDoc returns one published page. A draft is ErrNotFound, exactly as a
// missing page is, so a tenant cannot probe for unpublished slugs.
//
// GetAPIDoc 返回一个已发布页面。草稿与不存在的页面一样返回 ErrNotFound，使租户无法
// 探测未发布的 slug。
func (s *Service) GetAPIDoc(ctx context.Context, slug string) (APIDocDetail, error) {
	if !apidocs.ValidSlug(slug) {
		return APIDocDetail{}, ErrNotFound
	}
	row, err := s.store.GetAPIDoc(ctx, slug)
	if err != nil {
		return APIDocDetail{}, translate(err)
	}
	if row.Status != apidocs.StatusPublished {
		return APIDocDetail{}, ErrNotFound
	}
	content, err := apidocs.DecodeContent(row.ContentJSON)
	if err != nil {
		return APIDocDetail{}, errors.New("logic: stored api doc content is unreadable")
	}
	return APIDocDetail{APIDocSummary: apiDocSummary(row), Content: content}, nil
}

// OperatorListAPIDocs returns every page, drafts included.
//
// OperatorListAPIDocs 返回全部页面，包括草稿。
func (s *Service) OperatorListAPIDocs(ctx context.Context, actor Actor) ([]OperatorAPIDocSummary, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return nil, err
	}
	seeded, err := seededSlugs()
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListAPIDocs(ctx, false)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]OperatorAPIDocSummary, len(rows))
	for i, row := range rows {
		out[i] = operatorAPIDocSummary(row, seeded[row.Slug])
	}
	return out, nil
}

// OperatorGetAPIDoc returns one page, drafts included.
//
// OperatorGetAPIDoc 返回一个页面，包括草稿。
func (s *Service) OperatorGetAPIDoc(ctx context.Context, actor Actor, slug string) (OperatorAPIDocDetail, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return OperatorAPIDocDetail{}, err
	}
	if !apidocs.ValidSlug(slug) {
		return OperatorAPIDocDetail{}, ErrNotFound
	}
	row, err := s.store.GetAPIDoc(ctx, slug)
	if err != nil {
		return OperatorAPIDocDetail{}, translate(err)
	}
	return s.operatorAPIDocDetail(row)
}

// CreateAPIDoc creates a page under slug, owned by the operator from the
// start. ErrConflict if the slug exists.
//
// CreateAPIDoc 以 slug 创建一个页面，从一开始就归运维所有。slug 已存在时返回 ErrConflict。
func (s *Service) CreateAPIDoc(ctx context.Context, actor Actor, slug string, in APIDocInput) (OperatorAPIDocDetail, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return OperatorAPIDocDetail{}, err
	}
	row, err := s.operatorAPIDocRow(actor, slug, in)
	if err != nil {
		return OperatorAPIDocDetail{}, err
	}
	row.Version = 1
	audit := s.auditEntry(model.PlatformScope, actor.UserID, model.ActionAPIDocCreate, slug, "", actor.IP)
	if err := s.store.CreateAPIDoc(ctx, &row, &audit); err != nil {
		return OperatorAPIDocDetail{}, translate(err)
	}
	return s.operatorAPIDocDetail(row)
}

// UpdateAPIDoc replaces a page if its version is still expected. The page
// becomes operator-owned, so no later seed revision overwrites the edit.
//
// UpdateAPIDoc 在页面版本仍为 expected 时替换它。页面随之归运维所有，此后任何种子
// revision 都不会覆盖这次编辑。
func (s *Service) UpdateAPIDoc(ctx context.Context, actor Actor, slug string, expected int64, in APIDocInput) (OperatorAPIDocDetail, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return OperatorAPIDocDetail{}, err
	}
	if expected < 1 {
		return OperatorAPIDocDetail{}, ErrInvalidInput
	}
	row, err := s.operatorAPIDocRow(actor, slug, in)
	if err != nil {
		return OperatorAPIDocDetail{}, err
	}
	current, err := s.store.GetAPIDoc(ctx, slug)
	if err != nil {
		return OperatorAPIDocDetail{}, translate(err)
	}
	row.SeedRevision = current.SeedRevision
	audit := s.auditEntry(model.PlatformScope, actor.UserID, model.ActionAPIDocUpdate, slug, "", actor.IP)
	if err := s.store.UpdateAPIDoc(ctx, &row, expected, &audit); err != nil {
		return OperatorAPIDocDetail{}, translate(err)
	}
	return s.operatorAPIDocDetail(row)
}

// ResetAPIDoc discards an operator's edits and restores the compiled-in seed,
// handing the page back to seed refreshes. Its status is kept. ErrNotFound if
// slug has no seed.
//
// ResetAPIDoc 丢弃运维的编辑并恢复为编译内置的种子，把页面交还给种子刷新。状态保持
// 不变。slug 没有种子时返回 ErrNotFound。
func (s *Service) ResetAPIDoc(ctx context.Context, actor Actor, slug string, expected int64) (OperatorAPIDocDetail, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return OperatorAPIDocDetail{}, err
	}
	if expected < 1 {
		return OperatorAPIDocDetail{}, ErrInvalidInput
	}
	seed, ok, err := findSeed(slug)
	if err != nil {
		return OperatorAPIDocDetail{}, err
	}
	if !ok {
		return OperatorAPIDocDetail{}, ErrNotFound
	}
	current, err := s.store.GetAPIDoc(ctx, slug)
	if err != nil {
		return OperatorAPIDocDetail{}, translate(err)
	}
	encoded, err := apidocs.Validate(seed.Meta, seed.Content)
	if err != nil {
		return OperatorAPIDocDetail{}, err
	}
	row := apiDocRow(slug, seed.Meta, encoded)
	row.Status, row.Source, row.SeedRevision = current.Status, apidocs.SourceSeed, seed.Revision
	row.UpdatedBy, row.UpdatedAt = actor.UserID, s.clock.Now().UTC()
	audit := s.auditEntry(model.PlatformScope, actor.UserID, model.ActionAPIDocReset, slug, "", actor.IP)
	if err := s.store.UpdateAPIDoc(ctx, &row, expected, &audit); err != nil {
		return OperatorAPIDocDetail{}, translate(err)
	}
	return s.operatorAPIDocDetail(row)
}

// DeleteAPIDoc removes an operator-created page. A seeded page is
// ErrConflict: the next start would recreate it, so the way to hide one is
// to set it to draft.
//
// DeleteAPIDoc 删除一个运维创建的页面。有种子的页面返回 ErrConflict：下次启动会把它
// 重新建出来，因此隐藏它的方式是改为草稿。
func (s *Service) DeleteAPIDoc(ctx context.Context, actor Actor, slug string, expected int64) error {
	if err := s.requirePlatformActor(actor); err != nil {
		return err
	}
	if !apidocs.ValidSlug(slug) {
		return ErrNotFound
	}
	if expected < 1 {
		return ErrInvalidInput
	}
	_, seeded, err := findSeed(slug)
	if err != nil {
		return err
	}
	if seeded {
		return ErrConflict
	}
	audit := s.auditEntry(model.PlatformScope, actor.UserID, model.ActionAPIDocDelete, slug, "", actor.IP)
	return translate(s.store.DeleteAPIDoc(ctx, slug, expected, &audit))
}

func (s *Service) operatorAPIDocRow(actor Actor, slug string, in APIDocInput) (model.APIDoc, error) {
	if !apidocs.ValidSlug(slug) || !apidocs.ValidStatus(in.Status) {
		return model.APIDoc{}, ErrInvalidInput
	}
	encoded, err := apidocs.Validate(in.Meta, in.Content)
	if err != nil {
		return model.APIDoc{}, ErrInvalidInput
	}
	row := apiDocRow(slug, in.Meta, encoded)
	row.Status, row.Source, row.UpdatedBy, row.UpdatedAt = in.Status, apidocs.SourceOperator, actor.UserID, s.clock.Now().UTC()
	return row, nil
}

func (s *Service) operatorAPIDocDetail(row model.APIDoc) (OperatorAPIDocDetail, error) {
	content, err := apidocs.DecodeContent(row.ContentJSON)
	if err != nil {
		return OperatorAPIDocDetail{}, errors.New("logic: stored api doc content is unreadable")
	}
	_, seeded, err := findSeed(row.Slug)
	if err != nil {
		return OperatorAPIDocDetail{}, err
	}
	return OperatorAPIDocDetail{OperatorAPIDocSummary: operatorAPIDocSummary(row, seeded), Content: content}, nil
}

func apiDocRow(slug string, meta apidocs.Meta, contentJSON string) model.APIDoc {
	return model.APIDoc{
		Slug: slug, Category: meta.Category, CategoryOrder: meta.CategoryOrder, SortOrder: meta.SortOrder,
		Title: meta.Title, Method: meta.Method, Path: meta.Path, Summary: meta.Summary, ContentJSON: contentJSON,
	}
}

func apiDocSummary(row model.APIDoc) APIDocSummary {
	return APIDocSummary{
		Slug: row.Slug,
		Meta: apidocs.Meta{
			Category: row.Category, CategoryOrder: row.CategoryOrder, SortOrder: row.SortOrder,
			Title: row.Title, Method: row.Method, Path: row.Path, Summary: row.Summary,
		},
		UpdatedAt: row.UpdatedAt,
	}
}

func operatorAPIDocSummary(row model.APIDoc, seeded bool) OperatorAPIDocSummary {
	return OperatorAPIDocSummary{
		APIDocSummary: apiDocSummary(row), Status: row.Status, Source: row.Source,
		SeedRevision: row.SeedRevision, Version: row.Version, UpdatedBy: row.UpdatedBy, Seeded: seeded,
	}
}

func seededSlugs() (map[string]bool, error) {
	seeds, err := apidocs.Seeds()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		out[seed.Slug] = true
	}
	return out, nil
}

func findSeed(slug string) (apidocs.Seed, bool, error) {
	seeds, err := apidocs.Seeds()
	if err != nil {
		return apidocs.Seed{}, false, err
	}
	for _, seed := range seeds {
		if seed.Slug == slug {
			return seed, true, nil
		}
	}
	return apidocs.Seed{}, false, nil
}
