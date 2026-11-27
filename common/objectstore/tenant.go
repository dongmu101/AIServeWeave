package objectstore

import (
	"errors"
	"net/http"
	"sync"
)

var (
	guardedOnce    sync.Once
	guardedClients [2]*http.Client // [0] blocks private ranges, [1] allows them
)

// sharedGuardedClient returns one process-wide GuardedHTTPClient per policy,
// so every tenant backend shares a connection pool instead of opening its own.
//
// sharedGuardedClient 为每种策略返回一个进程级共享的 GuardedHTTPClient，使所有租户后端共用
// 连接池，而不是各开一个。
func sharedGuardedClient(allowPrivate bool) *http.Client {
	guardedOnce.Do(func() {
		guardedClients[0] = GuardedHTTPClient(false)
		guardedClients[1] = GuardedHTTPClient(true)
	})
	if allowPrivate {
		return guardedClients[1]
	}
	return guardedClients[0]
}

// NewTenant builds the backend a tenant configured: only "s3" or "webdav",
// whose endpoint passes CheckEndpoint and whose every connection goes through
// a GuardedHTTPClient with the same allowPrivate. Its HTTPClient fields are
// ignored. A tenant may not use "local": that would be the platform's own disk.
//
// NewTenant 构建租户配置的后端：只允许 "s3" 或 "webdav"，端点须通过 CheckEndpoint，每个
// 连接都经过同一 allowPrivate 的 GuardedHTTPClient。传入的 HTTPClient 字段被忽略。租户不能
// 使用 "local"：那是平台自己的磁盘。
func NewTenant(cfg Config, allowPrivate bool) (Backend, error) {
	client := sharedGuardedClient(allowPrivate)
	switch cfg.Kind {
	case "s3":
		if cfg.S3.Endpoint != "" {
			if err := CheckEndpoint(cfg.S3.Endpoint, allowPrivate); err != nil {
				return nil, err
			}
		}
		cfg.S3.HTTPClient = client
	case "webdav":
		if err := CheckEndpoint(cfg.WebDAV.URL, allowPrivate); err != nil {
			return nil, err
		}
		cfg.WebDAV.HTTPClient = client
	default:
		return nil, errors.New(`objectstore: a tenant's storage must be "s3" or "webdav"`)
	}
	return New(cfg)
}
