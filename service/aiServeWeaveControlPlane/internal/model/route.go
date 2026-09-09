package model

import "time"

// RouteRevision stores immutable scalar routing data. / RouteRevision 保存不可变的标量路由数据。
type RouteRevision struct {
	Revision   int64 `gorm:"primaryKey;autoIncrement:false"`
	Digest     string
	RoutesJSON string `gorm:"column:routes_json"`
	ActorID    string
	CreatedAt  time.Time
	RollbackOf int64
}

// RouteActive is the singleton publication pointer. / RouteActive 是发布指针的单例行。
type RouteActive struct {
	ID       int `gorm:"primaryKey;autoIncrement:false"`
	Revision int64
}
