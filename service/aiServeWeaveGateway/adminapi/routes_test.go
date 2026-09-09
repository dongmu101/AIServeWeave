package adminapi_test

import (
	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveGateway/adminapi"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestRoutesStatusAuthentication(t *testing.T) {
	handler, err := adminapi.New(adminapi.Config{Token: "secret", ReplicaID: "gateway", Nodes: func() []tunnelserver.NodeInfo { return nil }, Routes: func() modelroute.Applied {
		return modelroute.Applied{Mode: "controlplane", Revision: 7, Digest: "digest"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, token string
		want                int
	}{{"unauthenticated", "GET", "", 401}, {"bad credential", "GET", "Bearer bad", 401}, {"read", "GET", "Bearer secret", 200}, {"write denied", "POST", "Bearer secret", 405}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/internal/v1/routes", nil)
			req.Header.Set("Authorization", tc.token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("want status %d, got %d", tc.want, w.Code)
			}
			if w.Code == 200 {
				var got modelroute.ReplicaStatus
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Revision != 7 || got.ReplicaID != "gateway" || got.GeneratedAt.IsZero() {
					t.Fatalf("want revision 7 gateway identity and timestamp, got %+v", got)
				}
			}
		})
	}
}
