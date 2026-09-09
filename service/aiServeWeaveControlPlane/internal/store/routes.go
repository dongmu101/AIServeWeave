package store

import (
	"context"
	"errors"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// ErrRouteCapacity prevents unbounded immutable history. / ErrRouteCapacity 阻止不可变历史无限增长。
var ErrRouteCapacity = errors.New("store: route history capacity reached")

// Routes persists immutable revisions with atomic publication and audit. / Routes 持久化不可变版本，原子更新发布指针与审计。
type Routes interface {
	CurrentRouteRevision(context.Context) (model.RouteRevision, error)
	GetRouteRevision(context.Context, int64) (model.RouteRevision, error)
	ListRouteRevisions(context.Context, int64, int) ([]model.RouteRevision, error)
	PublishRouteRevision(context.Context, int64, *model.RouteRevision, *model.AuditLog) error
}
