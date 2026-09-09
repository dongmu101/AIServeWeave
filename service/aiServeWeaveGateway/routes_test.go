package main

import (
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"context"
	"testing"
	"time"
)

func TestRouteSourceConfiguration(t *testing.T) {
	t.Setenv(controlPlaneTokenEnv, "")
	for _, tc := range []struct {
		name, source, files, state, endpoint, token string
		interval                                    time.Duration
		wantErr                                     bool
	}{
		{name: "file default", source: "file", interval: time.Second},
		{name: "invalid source", source: "other", interval: time.Second, wantErr: true},
		{name: "invalid interval", source: "file", wantErr: true},
		{name: "managed conflicts with files", source: "controlplane", files: "r.json", interval: time.Second, wantErr: true},
		{name: "managed requires endpoint", source: "controlplane", state: "cache", token: "token", interval: time.Second, wantErr: true},
		{name: "managed requires token", source: "controlplane", state: "cache", endpoint: "http://localhost", interval: time.Second, wantErr: true},
		{name: "managed requires state", source: "controlplane", endpoint: "http://localhost", token: "token", interval: time.Second, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, syncer, err := configureRoutes(context.Background(), tc.source, tc.files, tc.state, tc.endpoint, tc.token, tc.interval, func(*routing.Table) {})
			if (err != nil) != tc.wantErr {
				t.Fatalf("want error=%v, got %v", tc.wantErr, err)
			}
			if err == nil {
				got := status()
				if syncer != nil || got.Mode != "file" || len(got.Digest) != 64 || got.AppliedAt.IsZero() {
					t.Fatalf("want file status and digest, got %+v", got)
				}
			}
		})
	}
}
