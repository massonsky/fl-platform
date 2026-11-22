package inference

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type Policy struct{ Allowed []string }

func (p Policy) Validate(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || len(raw) > 512 {
		return "", Failure{400, "INVALID_PROVIDER_URL"}
	}
	if u.Port() != "" && u.Port() != "443" {
		return "", Failure{400, "INVALID_PROVIDER_URL"}
	}
	if strings.Contains(u.Path, "..") || strings.Contains(u.Path, "//") || u.RawPath != "" {
		return "", Failure{400, "INVALID_PROVIDER_URL"}
	}
	normalized := strings.TrimRight(u.String(), "/")
	for _, allowed := range p.Allowed {
		if normalized == strings.TrimRight(strings.TrimSpace(allowed), "/") {
			return normalized, nil
		}
	}
	return "", Failure{400, "PROVIDER_NOT_ALLOWED"}
}

// Deny special-use ranges, including IPv4 embedded in IPv6 and NAT64.
func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("provider DNS unavailable")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errors.New("provider address forbidden")
		}
	}
	// Dial the validated address, never resolve the hostname a second time.
	dialer := net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

func newClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") },
		Transport: &http.Transport{Proxy: nil, DialContext: safeDial,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16384, DisableKeepAlives: true},
	}
}
