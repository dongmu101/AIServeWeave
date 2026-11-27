package objectstore_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

// newFakeS3Server is a minimal path-style S3 stand-in covering exactly the
// three operations S3 uses: PUT, GET and DELETE on /{bucket}/{key}. It does
// not check the Authorization header — nothing here tests SigV4 signing
// itself, only that objectstore.S3 issues the right request per method and
// interprets the response correctly, including S3's real "404 with an XML
// NoSuchKey error body" shape for a missing object.
//
// newFakeS3Server 是一个只覆盖 S3 用到的三个操作（对 /{bucket}/{key} 的 PUT、
// GET、DELETE）的最小 path-style S3 替身。它不检查 Authorization 头——这里
// 没有任何测试针对 SigV4 签名本身，只测试 objectstore.S3 是否为每个方法发出
// 正确的请求、并正确解读响应，包括对象缺失时 S3 真实的「404 加 XML NoSuchKey
// 错误体」形状。
func newFakeS3Server(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	store := map[string][]byte{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		if len(parts) != 2 {
			http.Error(w, "objectstore test fake: expected /{bucket}/{key}", http.StatusBadRequest)
			return
		}
		key := parts[1]

		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			mu.Lock()
			store[key] = body
			mu.Unlock()
			w.Header().Set("ETag", `"fake-etag"`)
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			mu.Lock()
			body, ok := store[key]
			mu.Unlock()
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
					`<Error><Code>NoSuchKey</Code><Message>no such key</Message>` +
					`<Key>` + key + `</Key><RequestId>1</RequestId><HostId>1</HostId></Error>`))
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)

		case http.MethodDelete:
			mu.Lock()
			delete(store, key)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "objectstore test fake: unsupported method "+r.Method, http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newS3Backend(t *testing.T) objectstore.Backend {
	t.Helper()
	srv := newFakeS3Server(t)
	b, err := objectstore.NewS3(objectstore.S3Config{
		Endpoint:        srv.URL,
		Bucket:          "test-bucket",
		AccessKeyID:     "test-access-key",
		SecretAccessKey: "test-secret-key",
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return b
}

func TestNewS3RejectsEmptyBucket(t *testing.T) {
	if _, err := objectstore.NewS3(objectstore.S3Config{}); err == nil {
		t.Fatal("NewS3 with empty Bucket: want error, got nil")
	}
}
