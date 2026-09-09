package model

import "time"

// WorkflowTemplateRevision stores one immutable, versioned workflow template
// (P03). Unlike RouteRevision, the primary key is composite: many templates
// each keep their own independent history, rather than one platform-wide
// table with a single counter.
//
// WorkflowTemplateRevision 保存一个不可变、已版本化的工作流模板（P03）。与
// RouteRevision 不同，主键是复合的：多个模板各自保有独立的历史，而不是一张
// 平台级、单一计数器的表。
type WorkflowTemplateRevision struct {
	TemplateID         string `gorm:"primaryKey;size:128"`
	Revision           int64  `gorm:"primaryKey;autoIncrement:false"`
	Digest             string
	Description        string
	InputsJSON         string `gorm:"column:inputs_json"`
	OutputsJSON        string `gorm:"column:outputs_json"`
	DependenciesJSON   string `gorm:"column:dependencies_json"`
	VisibleTenantsJSON string `gorm:"column:visible_tenants_json"`
	GraphJSON          string `gorm:"column:graph_json"`
	ActorID            string
	CreatedAt          time.Time
	RollbackOf         int64
}

// WorkflowTemplateActive is one template's publication pointer. Unlike
// RouteActive there is no singleton row: one row exists per template id,
// created on that template's first publish.
//
// WorkflowTemplateActive 是一个模板的发布指针。与 RouteActive 不同，这里没有
// 单例行：每个模板 id 存在一行，在其首次发布时创建。
type WorkflowTemplateActive struct {
	TemplateID string `gorm:"primaryKey;size:128"`
	Revision   int64
}
