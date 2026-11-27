package objectstore

import (
	"errors"
	"net"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	smithy "github.com/aws/smithy-go"
	"github.com/studio-b12/gowebdav"
)

// The sentinels below classify a failed remote call, so a caller can say
// why a storage is unusable without quoting the server's text. A backend's
// error wraps at most one of them; errors.Is tells which. ErrAddressNotAllowed
// (guard.go) is the fifth class.
//
// 下面的哨兵值对失败的远程调用分类，使调用方能说明存储为何不可用而不必引用服务端原文。
// 后端返回的错误最多包装其中一个，用 errors.Is 判断。ErrAddressNotAllowed（guard.go）是
// 第五类。
var (
	// ErrUnreachable: no connection, DNS failure or timeout.
	// ErrUnreachable：连不上、DNS 失败或超时。
	ErrUnreachable = errors.New("objectstore: storage unreachable")
	// ErrAuth: the server rejected the credentials.
	// ErrAuth：服务端拒绝了凭据。
	ErrAuth = errors.New("objectstore: credentials rejected")
	// ErrPermission: the credentials work but may not do this.
	// ErrPermission：凭据有效但没有该操作的权限。
	ErrPermission = errors.New("objectstore: permission denied")
	// ErrContainerNotFound: the bucket or WebDAV collection does not exist.
	// ErrContainerNotFound：bucket 或 WebDAV 集合不存在。
	ErrContainerNotFound = errors.New("objectstore: bucket or collection not found")
)

// opError is a backend failure: its text is already redacted, and it
// unwraps to its class (nil when unclassified).
//
// opError 是一次后端失败：其文本已经脱敏，Unwrap 返回它的分类（无法分类时为 nil）。
type opError struct {
	class error
	msg   string
}

func (e *opError) Error() string { return e.msg }
func (e *opError) Unwrap() error { return e.class }

// networkClass classifies the failures every backend shares: the guard's
// refusal and transport errors.
//
// networkClass 对所有后端共有的失败分类：拨号限制的拒绝与传输层错误。
func networkClass(err error) error {
	if errors.Is(err, ErrAddressNotAllowed) {
		return ErrAddressNotAllowed
	}
	var netErr net.Error
	var opErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) || errors.As(err, &netErr) {
		return ErrUnreachable
	}
	return nil
}

// s3Class classifies an S3 SDK error.
// s3Class 对 S3 SDK 错误分类。
func s3Class(err error) error {
	if class := networkClass(err); class != nil {
		return class
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidToken", "ExpiredToken", "AuthorizationHeaderMalformed", "InvalidSecurity":
			return ErrAuth
		case "AccessDenied", "AllAccessDisabled", "AccountProblem":
			return ErrPermission
		case "NoSuchBucket":
			return ErrContainerNotFound
		}
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.HTTPStatusCode() {
		case 401:
			return ErrAuth
		case 403:
			return ErrPermission
		}
	}
	return nil
}

// webdavClass classifies a gowebdav error. A 404 or 409 here means a
// collection is missing: an object that is missing is mapped to ErrNotFound
// before this is consulted.
//
// webdavClass 对 gowebdav 错误分类。这里的 404 或 409 表示集合不存在：对象不存在的情形在
// 调用本函数之前已映射为 ErrNotFound。
func webdavClass(err error) error {
	if class := networkClass(err); class != nil {
		return class
	}
	switch {
	case gowebdav.IsErrCode(err, 401):
		return ErrAuth
	case gowebdav.IsErrCode(err, 403):
		return ErrPermission
	case gowebdav.IsErrCode(err, 404), gowebdav.IsErrCode(err, 409):
		return ErrContainerNotFound
	}
	return nil
}
