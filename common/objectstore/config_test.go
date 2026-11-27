package objectstore_test

import (
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

func TestNewDispatchesOnKind(t *testing.T) {
	tests := []struct {
		name    string
		cfg     objectstore.Config
		wantErr bool
	}{
		{"local", objectstore.Config{Kind: "local", Local: objectstore.LocalConfig{Dir: t.TempDir()}}, false},
		{"s3", objectstore.Config{Kind: "s3", S3: objectstore.S3Config{Bucket: "b"}}, false},
		{"webdav", objectstore.Config{Kind: "webdav", WebDAV: objectstore.WebDAVConfig{URL: "http://example.invalid"}}, false},
		{"unknown", objectstore.Config{Kind: "ftp"}, true},
		{"empty", objectstore.Config{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := objectstore.New(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("New(%+v): want error, got nil", tc.cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%+v): %v", tc.cfg, err)
			}
			if b == nil {
				t.Fatalf("New(%+v): got nil Backend with no error", tc.cfg)
			}
		})
	}
}
