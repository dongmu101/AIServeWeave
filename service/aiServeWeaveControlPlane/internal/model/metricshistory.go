package model

import "time"

// MetricsHistoryPoint is one rolled-up (metric, labels, bucket) sample —
// Labels is a canonical "k1=v1,k2=v2" string (keys sorted), not JSON: every
// read is an exact-match filter, never a partial-label query, so there is
// nothing a JSON column would buy here.
//
// MetricsHistoryPoint 是一个 (metric, labels, bucket) 的汇总样本——Labels 是
// 规范化的 "k1=v1,k2=v2" 字符串(key 已排序)，不是 JSON：每次读取都是精确匹配，
// 从不做标签的部分查询，JSON 列在这里买不到任何好处。
type MetricsHistoryPoint struct {
	ID       int64     `gorm:"primaryKey;autoIncrement"`
	Metric   string    `gorm:"column:metric"`
	Labels   string    `gorm:"column:labels"`
	BucketAt time.Time `gorm:"column:bucket_at"`
	Value    float64   `gorm:"column:value"`
}

func (MetricsHistoryPoint) TableName() string { return "metrics_history_points" }
