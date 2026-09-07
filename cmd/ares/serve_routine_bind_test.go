package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Contract for the serve bind address (release readiness): the config's
// server.host is the REAL bind address, never a wildcard by accident. The
// introspect read side is unauthenticated, so an empty host must fall back to
// loopback and a wildcard host must survive round-trip so operators can opt in
// explicitly. IPv6 literals must come back bracketed from net.JoinHostPort.
func TestServerBindAddr(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		want string
	}{
		{
			name: "empty_host_falls_back_to_loopback",
			host: "",
			port: 8080,
			want: "localhost:8080",
		},
		{
			name: "explicit_wildcard_is_preserved",
			host: "0.0.0.0",
			port: 8080,
			want: "0.0.0.0:8080",
		},
		{
			name: "ipv6_loopback_is_bracketed",
			host: "::1",
			port: 5606,
			want: "[::1]:5606",
		},
		{
			name: "ipv6_wildcard_is_bracketed",
			host: "::",
			port: 8080,
			want: "[::]:8080",
		},
		{
			name: "named_host_is_kept_verbatim",
			host: "localhost",
			port: 9090,
			want: "localhost:9090",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serverBindAddr(tt.host, tt.port); got != tt.want {
				t.Errorf("serverBindAddr(%q, %d) = %q, want %q", tt.host, tt.port, got, tt.want)
			}
		})
	}
}

// --host must win over both SERVER_HOST and the YAML value, mirroring the
// --port precedence: the explicit argument is the most specific intent. Guards
// the ordering inside loadServeConfig, where the flag override has to sit
// AFTER LoadFromEnv or the env silently wins.
func TestServeHostFlagPrecedence(t *testing.T) {
	origHost, origURL := serveHost, serveLLMURL
	t.Cleanup(func() { serveHost, serveLLMURL = origHost, origURL })

	// Minimal-setup branch (--llm-url, no config file).
	serveLLMURL = "http://127.0.0.1:11434"
	serveHost = "0.0.0.0"
	cfg, err := loadServeConfig()
	if err != nil {
		t.Fatalf("loadServeConfig (minimal): %v", err)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("minimal setup Server.Host = %q, want the --host value", cfg.Server.Host)
	}

	// Config-file branch: --host must beat SERVER_HOST.
	serveLLMURL = ""
	path := filepath.Join(t.TempDir(), "ares.yaml")
	if err := os.WriteFile(path, []byte("server:\n  host: 10.0.0.1\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	origPath := serveConfigPath
	t.Cleanup(func() { serveConfigPath = origPath })
	serveConfigPath = path
	t.Setenv("SERVER_HOST", "192.168.1.1")
	serveHost = "127.0.0.1"
	cfg, err = loadServeConfig()
	if err != nil {
		t.Fatalf("loadServeConfig (file): %v", err)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("Server.Host = %q, want the --host value to beat SERVER_HOST and YAML", cfg.Server.Host)
	}

	// Without the flag, SERVER_HOST still applies.
	serveHost = ""
	cfg, err = loadServeConfig()
	if err != nil {
		t.Fatalf("loadServeConfig (env only): %v", err)
	}
	if cfg.Server.Host != "192.168.1.1" {
		t.Errorf("Server.Host = %q, want SERVER_HOST to beat YAML when no flag is set", cfg.Server.Host)
	}
}

// The startup console must print a usable URL: a wildcard bind advertises the
// loopback probe address instead of the literal 0.0.0.0.
func TestDisplayServeHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		addr string
		want string
	}{
		{
			name: "wildcard_displays_loopback_probe",
			host: "0.0.0.0",
			addr: "0.0.0.0:8080",
			want: "localhost:8080",
		},
		{
			name: "loopback_bind_displays_verbatim",
			host: "127.0.0.1",
			addr: "127.0.0.1:8080",
			want: "127.0.0.1:8080",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayServeHost(tt.host, tt.addr); got != tt.want {
				t.Errorf("displayServeHost(%q, %q) = %q, want %q", tt.host, tt.addr, got, tt.want)
			}
		})
	}
}
