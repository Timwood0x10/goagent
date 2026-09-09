package builtin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// MaxHTTPResponseBytes is the default cap on response body reads to prevent
// memory exhaustion from oversized responses.
const MaxHTTPResponseBytes = 10 * 1024 * 1024 // 10 MB

// MaxHTTPRedirects limits how many redirects the HTTP client will follow.
// Each hop is re-validated against the SSRF filter.
const MaxHTTPRedirects = 3

// ErrSSRFBlocked is returned when a URL targets a blocked address.
var ErrSSRFBlocked = errors.New("url targets a blocked address (private/loopback/link-local)")

// ErrUnsupportedScheme is returned when a URL uses a non-http(s) scheme.
var ErrUnsupportedScheme = errors.New("only http and https schemes are allowed")

// ValidateURL checks that a URL string uses an allowed scheme and does not
// resolve to a private, loopback, or link-local address. It defends against
// SSRF attacks targeting cloud metadata endpoints and internal services.
func ValidateURL(ctx context.Context, rawURL string) error {
	if rawURL == "" {
		return errors.New("url is required")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", rawURL, err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ErrUnsupportedScheme
	}

	host := parsed.Hostname()
	if host == "" {
		return errors.New("url has no host")
	}

	return checkHost(ctx, host)
}

// checkHost resolves the host and rejects private, loopback, or link-local IPs.
// Hostnames that resolve to multiple IPs are rejected if any IP is blocked.
func checkHost(ctx context.Context, host string) error {
	// Handle bracketed IPv6 form.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}

	// If the host is already an IP literal, validate it directly.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %s", ErrSSRFBlocked, ip)
		}
		return nil
	}

	// Resolve the hostname and reject if any resolved address is blocked.
	ipAddrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve host %q: %w", host, err)
	}
	for _, ipAddr := range ipAddrs {
		if isBlockedIP(ipAddr.IP) {
			return fmt.Errorf("%w: %s resolves to %s", ErrSSRFBlocked, host, ipAddr.IP)
		}
	}
	return nil
}

// isBlockedIP reports whether the IP is private, loopback, link-local, or
// otherwise unsuitable as an outbound destination from a tool. IPv4-mapped
// IPv6 addresses (e.g. "::ffff:127.0.0.1") are normalized to IPv4 first so
// they cannot bypass the loopback/private checks.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Normalize IPv4-mapped IPv6 to its 4-byte form so IsLoopback / IsPrivate
	// classify it correctly across Go versions.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsUnspecified() {
		return true
	}
	// CGNAT / carrier-grade NAT (RFC 6598): 100.64.0.0/10. Used by Kubernetes
	// pod CIDRs and ISP-grade NAT — reachable from inside a cluster but must
	// not be an SSRF target. Not covered by IsPrivate (which only checks
	// RFC 1918 ranges).
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// ssrfDialControl validates the resolved destination IP immediately before a
// TCP connection is established. Because the dialer resolves the hostname
// itself and then invokes Control with the resolved IP, this closes the TOCTOU
// window between checkHost's pre-check and the actual connection: a DNS
// rebinding attack cannot swap in a private IP after the pre-check passes.
func ssrfDialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("ssrf: parse dial address %q: %w", address, err)
	}
	if ip := net.ParseIP(host); isBlockedIP(ip) {
		return fmt.Errorf("%w: dial to %s", ErrSSRFBlocked, ip)
	}
	return nil
}

// SSRFDialer returns a *net.Dialer that re-validates the resolved destination
// IP at connect time via Control, defending against DNS rebinding. Use it (or
// SSRFTransport) for any outbound HTTP traffic originating from tools.
func SSRFDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: ssrfDialControl,
	}
}

// SSRFTransport returns an *http.Transport whose DialContext is backed by
// SSRFDialer, so every TCP connection — including each redirect hop — is
// re-validated against the SSRF block list at connect time. Proxy is
// explicitly disabled: inheriting ProxyFromEnvironment from
// http.DefaultTransport would let a proxy bypass the dial-time IP check.
func SSRFTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	t := base.Clone()
	t.DialContext = SSRFDialer().DialContext
	t.Proxy = nil // do not inherit ProxyFromEnvironment
	return t
}

// SSRFCheckRedirect returns an http.CheckRedirect function that caps the
// number of redirects and re-validates each destination URL against the SSRF
// filter. This prevents public URLs from redirecting to internal services.
func SSRFCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxHTTPRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxHTTPRedirects)
	}
	return ValidateURL(req.Context(), req.URL.String())
}
