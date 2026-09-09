package logic

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ErrRouteCapacity means history is full and publication was refused. / ErrRouteCapacity 表示历史已满，发布被拒绝。
var ErrRouteCapacity = store.ErrRouteCapacity

// CurrentRoutes returns revision zero before the first publication. / CurrentRoutes 在首次发布前返回零版本。
func (s *Service) CurrentRoutes(ctx context.Context) (modelroute.Snapshot, error) {
	row, err := s.store.CurrentRouteRevision(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return modelroute.Snapshot{Routes: []modelroute.Route{}}, nil
	}
	if err != nil {
		return modelroute.Snapshot{}, translate(err)
	}
	return routeSnapshot(row)
}

// RouteRevision reads one immutable historical snapshot. / RouteRevision 读取一个不可变历史快照。
func (s *Service) RouteRevision(ctx context.Context, revision int64) (modelroute.Snapshot, error) {
	if revision < 1 {
		return modelroute.Snapshot{}, ErrInvalidInput
	}
	row, err := s.store.GetRouteRevision(ctx, revision)
	if err != nil {
		return modelroute.Snapshot{}, translate(err)
	}
	return routeSnapshot(row)
}

// RouteHistory is a bounded page of publication metadata. / RouteHistory 是有界的发布元数据页。
type RouteHistory struct {
	Items      []modelroute.RevisionInfo `json:"items"`
	NextBefore int64                     `json:"next_before,omitempty"`
}

// RoutesHistory reads newest-first metadata without routing bodies. / RoutesHistory 按版本倒序读取元数据，不包含路由正文。
func (s *Service) RoutesHistory(ctx context.Context, before int64, limit int) (RouteHistory, error) {
	if before < 0 || limit < 1 || limit > 50 {
		return RouteHistory{}, ErrInvalidInput
	}
	rows, err := s.store.ListRouteRevisions(ctx, before, limit+1)
	if err != nil {
		return RouteHistory{}, translate(err)
	}
	out := RouteHistory{Items: []modelroute.RevisionInfo{}}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextBefore = rows[len(rows)-1].Revision
	}
	for _, row := range rows {
		out.Items = append(out.Items, routeInfo(row))
	}
	return out, nil
}

// PublishRoutes validates and atomically publishes a platform operator's bundle. / PublishRoutes 校验并原子发布平台运维的路由包。
func (s *Service) PublishRoutes(ctx context.Context, actor Actor, expected int64, routes []modelroute.Route) (modelroute.Snapshot, error) {
	if !isPlatformActor(actor) || actor.UserID == "" {
		return modelroute.Snapshot{}, ErrForbidden
	}
	if expected < 0 || routes == nil {
		return modelroute.Snapshot{}, ErrInvalidInput
	}
	body, err := modelroute.Canonical(routes)
	if err != nil {
		return modelroute.Snapshot{}, ErrInvalidInput
	}
	digest, err := modelroute.Digest(routes)
	if err != nil {
		return modelroute.Snapshot{}, ErrInvalidInput
	}
	return s.publishRoutes(ctx, actor, expected, model.RouteRevision{Digest: digest, RoutesJSON: string(body)}, "routes.publish")
}

// RollbackRoutes copies an old snapshot into a new monotonic revision. / RollbackRoutes 将旧快照复制到新的单调递增版本。
func (s *Service) RollbackRoutes(ctx context.Context, actor Actor, expected, revision int64) (modelroute.Snapshot, error) {
	if !isPlatformActor(actor) || actor.UserID == "" {
		return modelroute.Snapshot{}, ErrForbidden
	}
	if expected < 0 || revision < 1 {
		return modelroute.Snapshot{}, ErrInvalidInput
	}
	row, err := s.store.GetRouteRevision(ctx, revision)
	if err != nil {
		return modelroute.Snapshot{}, translate(err)
	}
	if _, err := routeSnapshot(row); err != nil {
		return modelroute.Snapshot{}, err
	}
	row.RollbackOf = revision
	return s.publishRoutes(ctx, actor, expected, row, "routes.rollback")
}
func (s *Service) publishRoutes(ctx context.Context, actor Actor, expected int64, row model.RouteRevision, action string) (modelroute.Snapshot, error) {
	row.Revision = expected + 1
	row.CreatedAt = s.clock.Now().UTC()
	row.ActorID = actor.UserID
	audit := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: model.PlatformScope, ActorID: actor.UserID, Action: action, Target: strconv.FormatInt(row.Revision, 10), Detail: "routing revision published", IP: actor.IP, CreatedAt: row.CreatedAt}
	if err := s.store.PublishRouteRevision(ctx, expected, &row, &audit); err != nil {
		return modelroute.Snapshot{}, translate(err)
	}
	return routeSnapshot(row)
}
func routeInfo(row model.RouteRevision) modelroute.RevisionInfo {
	return modelroute.RevisionInfo{Revision: row.Revision, Digest: row.Digest, ActorID: row.ActorID, CreatedAt: row.CreatedAt, RollbackOf: row.RollbackOf}
}
func routeSnapshot(row model.RouteRevision) (modelroute.Snapshot, error) {
	out := modelroute.Snapshot{RevisionInfo: routeInfo(row)}
	if len(row.RoutesJSON) > modelroute.MaxRoutesBytes {
		return modelroute.Snapshot{}, errors.New("logic: invalid stored route size")
	}
	if err := json.Unmarshal([]byte(row.RoutesJSON), &out.Routes); err != nil {
		return modelroute.Snapshot{}, errors.New("logic: invalid stored routes")
	}
	if out.Routes == nil {
		return modelroute.Snapshot{}, errors.New("logic: invalid stored routes")
	}
	digest, err := modelroute.Digest(out.Routes)
	if err != nil || digest != row.Digest {
		return modelroute.Snapshot{}, errors.New("logic: invalid stored routes")
	}
	return out, nil
}
