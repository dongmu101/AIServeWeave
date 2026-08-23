package tunnelserver

import (
	"context"
	"crypto/x509"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"AIServeWeave/common/nodeid"
)

// peerNodeID returns the node identity asserted by the client certificate on
// ctx's connection.
//
// It reads only VerifiedChains, never PeerCertificates: VerifiedChains is
// populated by the TLS stack after a certificate has been checked against the
// configured CA pool, while PeerCertificates holds whatever the client sent.
// Reading the latter would let anyone claim any node_id by presenting a
// self-signed certificate, so a replica configured without client
// verification authenticates nobody rather than trusting everybody.
func peerNodeID(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "no peer information on the connection")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "connection is not mutually authenticated TLS")
	}
	leaf := verifiedLeaf(tlsInfo)
	if leaf == nil {
		return "", status.Error(codes.Unauthenticated, "no verified client certificate")
	}
	id, err := nodeid.FromCertificate(leaf)
	if err != nil {
		return "", status.Errorf(codes.Unauthenticated,
			"client certificate carries no node identity: expected a %s", nodeid.Expected())
	}
	return id, nil
}

// verifiedLeaf returns the leaf of the first verified chain, or nil when the
// handshake produced none.
func verifiedLeaf(info credentials.TLSInfo) *x509.Certificate {
	for _, chain := range info.State.VerifiedChains {
		if len(chain) > 0 && chain[0] != nil {
			return chain[0]
		}
	}
	return nil
}
