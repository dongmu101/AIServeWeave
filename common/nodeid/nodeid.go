// Package nodeid defines the single convention by which a node certificate
// asserts which node it belongs to. All three sides depend on it and must read
// it the same way: the Agent puts the identity into its CSR, the Registry signs
// a certificate carrying it, and every Gateway replica compares it against the
// node_id declared on a tunnel stream. A second, subtly different parser on any
// one of them would be an authentication bug, so there is only this one.
package nodeid

import (
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
)

// URIScheme is the scheme of the canonical URI SAN carried by a node
// certificate: aiserveweave://node/<node_id>. A URI SAN is used because a
// node_id is an operator-chosen label that need not be a legal DNS name; a DNS
// SAN equal to the node_id is accepted as well for CAs that issue one.
const URIScheme = "aiserveweave"

// URIHost is the authority of the canonical URI SAN, kept constant so the
// node_id is unambiguously the path component.
const URIHost = "node"

// ErrNoNodeIdentity is returned by FromCertificate when a certificate carries
// neither the canonical URI SAN nor exactly one DNS SAN. It is a sentinel so
// callers can wrap it in their own error model — the Agent reports a fatal
// identity error, the Gateway rejects the stream — without matching on text.
var ErrNoNodeIdentity = errors.New("nodeid: certificate carries no node identity")

// URI returns the canonical URI SAN for nodeID.
func URI(nodeID string) *url.URL {
	return &url.URL{Scheme: URIScheme, Host: URIHost, Path: "/" + nodeID}
}

// FromCertificate extracts the node identity a certificate asserts: the
// canonical URI SAN when present, otherwise a single DNS SAN. The common name
// is deliberately not accepted as a fallback — it is not a SAN, and the
// Gateway's check is defined against SANs. A certificate with several DNS SANs
// and no URI SAN is rejected rather than guessed at: picking one of them would
// let a certificate assert more than one identity.
func FromCertificate(leaf *x509.Certificate) (string, error) {
	if leaf == nil {
		return "", ErrNoNodeIdentity
	}
	for _, u := range leaf.URIs {
		if u == nil || u.Scheme != URIScheme || u.Host != URIHost {
			continue
		}
		if id := strings.TrimPrefix(u.Path, "/"); id != "" {
			return id, nil
		}
	}
	if len(leaf.DNSNames) == 1 && leaf.DNSNames[0] != "" {
		return leaf.DNSNames[0], nil
	}
	return "", ErrNoNodeIdentity
}

// Expected renders the identity forms FromCertificate accepts, for use in an
// error message when it finds neither.
func Expected() string {
	return URIScheme + "://" + URIHost + "/<node_id> URI SAN or exactly one DNS SAN"
}
