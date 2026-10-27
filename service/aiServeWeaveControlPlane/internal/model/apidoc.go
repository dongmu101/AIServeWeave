package model

import "time"

// APIDoc is one tenant-facing API reference page. Its body is stored as one
// JSON column because the page is always read and written whole; the columns
// beside it are what the navigation list needs without decoding any body.
//
// APIDoc 是一页面向租户的 API 参考文档。正文以单个 JSON 列存储，因为页面总是整体
// 读写；它旁边的那些列，是导航列表无需解码任何正文就要用到的部分。
type APIDoc struct {
	Slug          string `gorm:"primaryKey;size:128"`
	Category      string
	CategoryOrder int64
	SortOrder     int64
	Title         string
	Method        string
	Path          string
	Summary       string
	ContentJSON   string `gorm:"column:content_json"`
	// Status is apidocs.StatusPublished or StatusDraft. / Status 取 apidocs.StatusPublished 或 StatusDraft。
	Status string
	// Source is apidocs.SourceSeed or SourceOperator. / Source 取 apidocs.SourceSeed 或 SourceOperator。
	Source string
	// SeedRevision is the seed revision this row was last written from, or 0
	// for a document an operator created. / SeedRevision 是本行最近一次写入
	// 所依据的种子 revision；运维新建的文档为 0。
	SeedRevision int64
	// Version increments on every write and is the operator CAS token.
	// / Version 每次写入递增，是运维写入的 CAS 令牌。
	Version   int64
	UpdatedBy string
	UpdatedAt time.Time `gorm:"autoUpdateTime:false"`
}

// TableName pins the table name. / TableName 固定表名。
func (APIDoc) TableName() string { return "api_docs" }
