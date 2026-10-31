package registryclient

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

type mintClient struct {
	tunnelv1.TokenAdminClient
	t        *testing.T
	response *tunnelv1.MintTokenResponse
	calls    int
}

func (m *mintClient) MintToken(ctx context.Context, req *tunnelv1.MintTokenRequest, _ ...grpc.CallOption) (*tunnelv1.MintTokenResponse, error) {
	m.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer admin-test" {
		m.t.Errorf("authorization=%v, want configured bearer", got)
	}
	if _, ok := ctx.Deadline(); !ok {
		m.t.Error("mint context has no timeout")
	}
	if req.GetNodeId() != "new-node" || req.GetTtl().AsDuration() != time.Hour {
		m.t.Errorf("mint request=%v, want bound new-node and 1h", req)
	}
	return m.response, nil
}

// TestMintNodeTokenRequiresBindingAndPreservesRegistryExpiry checks the authenticated mint contract.
//
// TestMintNodeTokenRequiresBindingAndPreservesRegistryExpiry 检查签发鉴权契约。
func TestMintNodeTokenRequiresBindingAndPreservesRegistryExpiry(t *testing.T) {
	expiry := time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)
	upstream := &mintClient{t: t, response: &tunnelv1.MintTokenResponse{Token: "one-time-test", ExpiresAt: timestamppb.New(expiry)}}
	c := &Client{admin: upstream, adminToken: "admin-test", timeout: time.Second}
	for _, input := range []struct {
		name string
		node string
		ttl  time.Duration
	}{{"missing binding", "", time.Hour}, {"nonpositive TTL", "new-node", 0}} {
		t.Run(input.name, func(t *testing.T) {
			if _, _, err := c.MintNodeToken(t.Context(), input.node, input.ttl); err == nil {
				t.Fatal("MintNodeToken error=nil, want missing binding/TTL refusal")
			}
		})
	}
	if upstream.calls != 0 {
		t.Fatalf("calls=%d, want zero invalid-input calls", upstream.calls)
	}
	value, gotExpiry, err := c.MintNodeToken(t.Context(), "new-node", time.Hour)
	if err != nil || value != "one-time-test" || !gotExpiry.Equal(expiry) {
		t.Fatalf("token=%q expiry=%v err=%v, want issued token and Registry expiry", value, gotExpiry, err)
	}
	upstream.response = &tunnelv1.MintTokenResponse{Token: "one-time-test"}
	if _, _, err := c.MintNodeToken(t.Context(), "new-node", time.Hour); err == nil {
		t.Fatal("MintNodeToken error=nil, want malformed timestamp refusal")
	}
}
