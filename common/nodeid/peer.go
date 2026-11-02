package nodeid

import (
	"context"
	"crypto/x509"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// FromPeer returns the node identity asserted by the client certificate on
// ctx's gRPC connection.
//
// It reads only VerifiedChains, never PeerCertificates: VerifiedChains is
// populated by the TLS stack after a certificate has been checked against the
// configured CA pool, while PeerCertificates holds whatever the client sent.
// Reading the latter would let anyone claim any node_id by presenting a
// self-signed certificate, so a server configured without client verification
// authenticates nobody rather than trusting everybody. Both the Gateway (a
// tunnel stream's node_id) and the Registry (RenewCertificate's caller)
// depend on this exact behavior, which is why it lives here instead of being
// implemented twice.
func FromPeer(ctx context.Context) (string, error) {
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
	id, err := FromCertificate(leaf)
	if err != nil {
		return "", status.Errorf(codes.Unauthenticated,
			"client certificate carries no node identity: expected a %s", Expected())
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

// PublicKeyFromPeer returns the DER SubjectPublicKeyInfo of the verified client
// certificate on ctx's connection — the node key a sealed remote provider
// bundle is addressed to. The caller must still check it against the
// Registry's fingerprint before trusting it for anything.
//
// PublicKeyFromPeer 返回 ctx 所在连接上已验证客户端证书的 DER SubjectPublicKeyInfo——
// 即远程供应商密文所发往的节点密钥。调用方在把它用于任何用途之前，仍须用 Registry 的
// 指纹核对它。
func PublicKeyFromPeer(ctx context.Context) ([]byte, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no peer information on the connection")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "connection is not mutually authenticated TLS")
	}
	leaf := verifiedLeaf(tlsInfo)
	if leaf == nil {
		return nil, status.Error(codes.Unauthenticated, "no verified client certificate")
	}
	return append([]byte(nil), leaf.RawSubjectPublicKeyInfo...), nil
}
