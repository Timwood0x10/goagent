package builtin

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsBlockedIP verifies that isBlockedIP classifies private, loopback,
// link-local, and unspecified IPs as blocked — including IPv4-mapped IPv6
// addresses that previously bypassed the loopback/private checks.
func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		blocked bool
	}{
		{name: "ipv4-mapped loopback", addr: "::ffff:127.0.0.1", blocked: true},
		{name: "ipv4-mapped link-local", addr: "::ffff:169.254.1.1", blocked: true},
		{name: "ipv4-mapped public", addr: "::ffff:8.8.8.8", blocked: false},
		{name: "ipv4-mapped private", addr: "::ffff:192.168.1.1", blocked: true},
		{name: "ipv4 unspecified", addr: "0.0.0.0", blocked: true},
		{name: "ipv4 loopback", addr: "127.0.0.1", blocked: true},
		{name: "ipv4 public", addr: "8.8.8.8", blocked: false},
		{name: "ipv6 loopback", addr: "::1", blocked: true},
		{name: "ipv4 private rfc1918", addr: "192.168.1.1", blocked: true},
		{name: "ipv4 private rfc1918 10", addr: "10.0.0.1", blocked: true},
		{name: "ipv4 public cloudflare", addr: "1.1.1.1", blocked: false},
		{name: "ipv4 link-local", addr: "169.254.1.1", blocked: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.addr)
			require.NotNil(t, ip, "input %q must parse as IP", tt.addr)
			require.Equal(t, tt.blocked, isBlockedIP(ip), "isBlockedIP(%s)", tt.addr)
		})
	}
}

// TestSSRFDialControl exercises the connect-time validation directly, without
// any network. This is the connect-time mechanism: the dialer's Control callback fires
// with the resolved IP and re-runs isBlockedIP, closing the DNS-rebinding
// TOCTOU window.
func TestSSRFDialControl(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		wantBlocked bool
	}{
		{name: "loopback ipv4", addr: "127.0.0.1:80", wantBlocked: true},
		{name: "public ipv4", addr: "8.8.8.8:80", wantBlocked: false},
		{name: "private rfc1918", addr: "192.168.1.1:80", wantBlocked: true},
		{name: "ipv4-mapped loopback", addr: "[::ffff:127.0.0.1]:80", wantBlocked: true},
		{name: "ipv6 loopback", addr: "[::1]:80", wantBlocked: true},
		{name: "public cloudflare https", addr: "1.1.1.1:443", wantBlocked: false},
		{name: "unspecified ipv4", addr: "0.0.0.0:80", wantBlocked: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ssrfDialControl("tcp", tt.addr, nil)
			if tt.wantBlocked {
				require.Error(t, err, "expected dial to %s to be blocked", tt.addr)
				require.ErrorIs(t, err, ErrSSRFBlocked, "expected ErrSSRFBlocked for %s", tt.addr)
				return
			}
			require.NoError(t, err, "expected dial to %s to be allowed", tt.addr)
		})
	}
}

// TestSSRFDialControl_MalformedAddress ensures a non-IP, non-host:port string
// surfaces a wrapped error rather than panicking.
func TestSSRFDialControl_MalformedAddress(t *testing.T) {
	err := ssrfDialControl("tcp", "no-port-here", nil)
	require.Error(t, err)
	// Malformed input fails at SplitHostPort, before isBlockedIP, so it must
	// NOT wrap ErrSSRFBlocked.
	require.False(t, errors.Is(err, ErrSSRFBlocked))
}

// TestSSRFDialer_BlocksLoopback drives the full dialer end-to-end. Control
// fires before any TCP connect is attempted, so no listener is required and
// the test is fully offline.
func TestSSRFDialer_BlocksLoopback(t *testing.T) {
	_, err := SSRFDialer().DialContext(context.Background(), "tcp", "127.0.0.1:80")
	require.ErrorIs(t, err, ErrSSRFBlocked)

	_, err = SSRFDialer().DialContext(context.Background(), "tcp", "192.168.1.1:80")
	require.ErrorIs(t, err, ErrSSRFBlocked)
}

// TestSSRFTransport_ReturnsClonedTransport verifies SSRFTransport wires a
// non-nil DialContext backed by SSRFDialer, without performing any network.
func TestSSRFTransport_ReturnsClonedTransport(t *testing.T) {
	tr := SSRFTransport()
	require.NotNil(t, tr)
	require.NotNil(t, tr.DialContext, "SSRFTransport must set DialContext")
}

// TestSSRF_Rebinding_nipio is a network-dependent test that uses nip.io to
// prove DNS-rebinding defense end-to-end. It is skipped under -short and when
// nip.io is not resolvable, so it never breaks offline CI / `make check`.
func TestSSRF_Rebinding_nipio(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent DNS rebinding test in -short mode")
	}
	// Precheck: skip if nip.io cannot be resolved (offline CI).
	addrs, err := net.DefaultResolver.LookupHost(context.Background(), "127.0.0.1.nip.io")
	if err != nil {
		t.Skipf("skipping: nip.io not resolvable: %v", err)
	}
	// Verify that at least one resolved IP is actually a blocked IP.
	// nip.io may not resolve to 127.0.0.1 in all environments.
	hasBlocked := false
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip != nil && isBlockedIP(ip) {
			hasBlocked = true
			break
		}
	}
	if !hasBlocked {
		t.Skipf("skipping: nip.io resolved to non-blocked IPs %v", addrs)
	}

	_, err = SSRFDialer().DialContext(context.Background(), "tcp", "127.0.0.1.nip.io:80")
	require.ErrorIs(t, err, ErrSSRFBlocked)

	// Public rebinding host must NOT be blocked at the control layer.
	require.NoError(t, ssrfDialControl("tcp", "1.1.1.1.nip.io:80", nil))
}
