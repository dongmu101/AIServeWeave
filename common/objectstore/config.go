package objectstore

import "fmt"

// Config selects and configures one Backend. Kind names which of Local, S3
// or WebDAV field to build; the other two fields are ignored. This is a
// convenience for a caller (Gateway's main.go) that has one configured
// backend chosen at deploy time — it does not itself parse flags or read
// secret files, so it stays free of that caller's own conventions for doing
// so.
//
// Config 选择并配置一个 Backend。Kind 指出要用 Local、S3、WebDAV 三个字段中的
// 哪一个来构建；另外两个字段被忽略。这是给调用方（Gateway 的 main.go）用的
// 便利封装——调用方在部署时选定唯一一种后端；本函数自己不解析 flag 也不读取
// 密钥文件，因此不会牵扯进调用方自己那套约定。
type Config struct {
	// Kind is "local", "s3" or "webdav".
	//
	// Kind 取值为 "local"、"s3" 或 "webdav"。
	Kind   string
	Local  LocalConfig
	S3     S3Config
	WebDAV WebDAVConfig
}

// New builds the Backend cfg.Kind names.
//
// New 构建 cfg.Kind 指定的 Backend。
func New(cfg Config) (Backend, error) {
	switch cfg.Kind {
	case "local":
		return NewLocal(cfg.Local)
	case "s3":
		return NewS3(cfg.S3)
	case "webdav":
		return NewWebDAV(cfg.WebDAV)
	default:
		return nil, fmt.Errorf("objectstore: unknown backend kind %q (want \"local\", \"s3\" or \"webdav\")", cfg.Kind)
	}
}
