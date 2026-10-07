package netguard

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestBlockReasonTable is the core of the guard: publicly routable addresses
// pass, the internal/metadata range does not, and the escape hatch opens.
func TestBlockReasonTable(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want string // "" means allowed
	}{
		{"public IPv4", "93.184.216.34", ""},
		{"public IPv6", "2606:2800:220:1:248:1893:25c8:1946", ""},
		{"loopback v4", "127.0.0.1", "loopback"},
		{"loopback v6", "::1", "loopback"},
		{"RFC1918 10/8", "10.0.0.1", "private (RFC1918 or IPv6 unique-local)"},
		{"RFC1918 172.16/12", "172.16.0.9", "private (RFC1918 or IPv6 unique-local)"},
		{"RFC1918 192.168/16", "192.168.1.1", "private (RFC1918 or IPv6 unique-local)"},
		{"cloud metadata", "169.254.169.254", "link-local (includes cloud metadata 169.254.169.254)"},
		{"IPv6 unique-local fd00::/8", "fd00::1", "private (RFC1918 or IPv6 unique-local)"},
		{"IPv4-mapped loopback", "::ffff:127.0.0.1", "loopback"},
		{"unspecified v4", "0.0.0.0", "unspecified"},
		{"unspecified v6", "::", "unspecified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("ParseIP(%q) = nil", tt.ip)
			}
			if got := blockReason(ip); got != tt.want {
				t.Errorf("blockReason(%s) = %q, want %q", tt.ip, got, tt.want)
			}
		})
	}
}

// TestAllowPrivateSwitch proves the single escape hatch opens every refusal —
// and that it starts closed (the shipped default is BLOCKED).
func TestAllowPrivateSwitch(t *testing.T) {
	if AllowPrivate {
		t.Fatal("AllowPrivate defaults to true; the shipped binary must default to blocked")
	}
	ip := net.ParseIP("169.254.169.254")
	if got := blockReason(ip); got == "" {
		t.Fatal("metadata address is allowed by default")
	}
	AllowPrivate = true
	defer func() { AllowPrivate = false }()
	if got := blockReason(ip); got != "" {
		t.Fatalf("AllowPrivate=true still blocks: %q", got)
	}
	if err := checkIP(ip); err != nil {
		t.Fatalf("checkIP with AllowPrivate=true = %v", err)
	}
}

// TestDialContextRefusesLoopback proves the guard at the dialer itself, which
// is where the http.Transport applies it.
func TestDialContextRefusesLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Default: blocked, with a refusal that names the reason and the flag.
	_, err = DialContext(ctx, "tcp", ln.Addr().String())
	if err == nil {
		t.Fatal("DialContext to 127.0.0.1 succeeded; SSRF guard is not active")
	}
	if !strings.Contains(err.Error(), "loopback") || !strings.Contains(err.Error(), "--allow-private-fetch") {
		t.Errorf("refusal should name the reason and the escape hatch, got %q", err)
	}

	// Hostname form resolves first, then is checked the same way.
	_, err = DialContext(ctx, "tcp", net.JoinHostPort("localhost", portOf(t, ln.Addr().String())))
	if err == nil {
		t.Fatal("DialContext to localhost succeeded; SSRF guard is not active")
	}

	// Escape hatch: the same dial succeeds.
	AllowPrivate = true
	defer func() { AllowPrivate = false }()
	conn, err := DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext with AllowPrivate=true: %v", err)
	}
	conn.Close()
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	return port
}
