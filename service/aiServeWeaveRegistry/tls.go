package main

import (
	"crypto/tls"
	"net"
	"strings"

	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
)

// tlsCertificate pairs certPEM and keyPEM into a tls.Certificate.
func tlsCertificate(certPEM, keyPEM []byte) (tls.Certificate, error) {
	return tls.X509KeyPair(certPEM, keyPEM)
}

// tlsConfig builds the Registry listener's TLS configuration: cert is
// presented to every caller, and a caller's own client certificate — if it
// presents one — is verified against this Registry's own CA, since a node
// certificate has no other issuer.
func tlsConfig(cert tls.Certificate, root *ca.CA) tls.Config {
	return tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    root.Pool(),
	}
}

// splitCommaList parses a comma-separated flag value, trimming whitespace
// and dropping empty entries.
func splitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// addrHost extracts the host portion of a listen address (e.g. ":9090" ->
// "localhost"), for use as the DNS SAN on a self-issued server certificate
// when no -tls-host was given.
func addrHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return "localhost"
	}
	return host
}
