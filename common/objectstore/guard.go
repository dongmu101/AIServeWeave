package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrAddressNotAllowed is returned, wrapped, when a GuardedHTTPClient refuses
// to connect to an address in a range it blocks, and by CheckEndpoint for a
// URL it refuses outright.
//
// ErrAddressNotAllowed 在 GuardedHTTPClient 拒绝连接被屏蔽网段内的地址时（经包装）
// 返回，CheckEndpoint 直接拒绝某个 URL 时也返回它。
var ErrAddressNotAllowed = errors.New("objectstore: address not allowed")

// blockedPrefixes are the ranges a tenant's storage endpoint may not reach
// unless the operator allows private endpoints: this host, private and
// carrier-grade NAT networks, link-local (including cloud metadata at
// 169.254.169.254), multicast and reserved space. They are where a
// deployment's own services live, so a tenant must not be able to point the
// platform at them.
//
// blockedPrefixes 是租户存储端点在运维未放行内网时不能到达的网段：本机、私网与运营商级
// NAT、链路本地（含云元数据地址 169.254.169.254）、组播与保留地址。部署自己的服务就在这些
// 地址上，因此不能让租户把平台指向它们。
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// AddressAllowed reports whether a guarded client may connect to ip. An
// IPv4-mapped IPv6 address is judged as the IPv4 address it carries.
//
// AddressAllowed 报告受限客户端能否连接 ip。IPv4 映射的 IPv6 地址按其承载的 IPv4 地址判断。
func AddressAllowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// GuardedHTTPClient returns a client for storage a tenant configured. Unless
// allowPrivate is set, its dialer refuses any resolved address AddressAllowed
// rejects. The check runs on the address actually dialed, so a hostname that
// resolves (or re-resolves) to an internal address and a redirect to one are
// both refused. It ignores proxy environment variables: a proxy would be the
// address dialed, and the check would say nothing about the real target.
//
// GuardedHTTPClient 返回用于租户自配存储的客户端。除非 allowPrivate 为真，其拨号器拒绝
// AddressAllowed 不允许的任何解析结果。检查作用于实际拨号的地址，因此解析（或重新解析）到
// 内网地址的主机名、以及跳转到内网地址的请求都会被拒绝。它忽略代理环境变量：否则拨号的是代理，
// 检查对真正的目标毫无意义。
func GuardedHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			addrPort, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrAddressNotAllowed, address)
			}
			if !AddressAllowed(addrPort.Addr()) {
				return fmt.Errorf("%w: %s", ErrAddressNotAllowed, addrPort.Addr())
			}
			return nil
		}
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	return &http.Client{Transport: transport}
}

// CheckEndpoint rejects a tenant-supplied endpoint URL before any connection:
// it must be an absolute http(s) URL with a host and no credentials, query or
// fragment; plain http and a literal IP in a blocked range are allowed only
// with allowPrivate. A hostname is not resolved here: GuardedHTTPClient
// checks what it resolves to when it connects.
//
// CheckEndpoint 在建立任何连接之前拒绝租户提供的端点 URL：它必须是带主机名、不含凭据、
// 查询串与片段的绝对 http(s) URL；明文 http 与落在屏蔽网段内的 IP 字面量只在 allowPrivate
// 时允许。这里不解析主机名：GuardedHTTPClient 在连接时检查解析结果。
func CheckEndpoint(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("objectstore: endpoint must be an absolute URL with a host and no credentials, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowPrivate {
			return fmt.Errorf("%w: plain http", ErrAddressNotAllowed)
		}
	default:
		return errors.New("objectstore: endpoint must use https")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !allowPrivate && !AddressAllowed(ip) {
		return fmt.Errorf("%w: %s", ErrAddressNotAllowed, ip)
	}
	return nil
}
