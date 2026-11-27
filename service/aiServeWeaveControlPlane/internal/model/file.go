package model

import "time"

// File storage locations: the deployment's own object storage or the
// tenant's.
//
// 文件所在的存储：部署自己的对象存储，或租户的自有存储。
const (
	FileStorageDeployment = "deployment"
	FileStorageTenant     = "tenant"
)

// File statuses. A file is registered pending before its bytes are written
// and becomes ready once they are, so an object a crash leaves behind is
// still on record.
//
// 文件状态。写入字节之前先登记为 pending，写完后变为 ready，因此崩溃留下的对象也有记录。
const (
	FileStatusPending = "pending"
	FileStatusReady   = "ready"
)

// File business types, the kind of object BizID names.
//
// 文件归属的业务类型，即 BizID 指向的对象种类。
const (
	FileBizJob       = "job"
	FileBizStudioRun = "studio_run"
)

// File is one object the platform wrote to object storage. ID is the file's
// own public id (a job artifact's id, or a Studio file's id), so a Gateway
// retrying a write registers the same row again. The table is partitioned
// by CreatedAt, so the primary key is (ID, CreatedAt) and a registration
// keeps the CreatedAt of the row it replaces. The indexes are created by
// the migration only; see .scratch/file-registry/spec.md.
//
// File 是平台写进对象存储的一个对象。ID 是文件自己的公开 id（job 产物的 id 或 Studio
// 文件的 id），因此 Gateway 重试写入时登记的是同一行。表按 CreatedAt 分区，主键为
// (ID, CreatedAt)，重新登记时保留原行的 CreatedAt。索引只由迁移创建，见
// .scratch/file-registry/spec.md。
type File struct {
	ID          string    `gorm:"primaryKey;size:64"`
	TenantID    string    `gorm:"size:40;not null"`
	Storage     string    `gorm:"size:16;not null"`
	ObjectKey   string    `gorm:"size:1024;not null"`
	BizType     string    `gorm:"size:32;not null"`
	BizID       string    `gorm:"size:64;not null"`
	Kind        string    `gorm:"size:32;not null"`
	Filename    string    `gorm:"size:512;not null"`
	ContentType string    `gorm:"size:255;not null"`
	SizeBytes   int64     `gorm:"not null"`
	SHA256      string    `gorm:"size:64;not null"`
	Status      string    `gorm:"size:16;not null"`
	CreatedAt   time.Time `gorm:"primaryKey;not null;autoCreateTime:false"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime:false"`
}

// TableName pins the table name, as Tenant.TableName does.
//
// TableName 钉死表名，与 Tenant.TableName 同理。
func (File) TableName() string { return "files" }
