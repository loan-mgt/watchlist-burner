package jellyfin

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestValidateURL(t *testing.T) {
	public := NewGuard(false, false, nil)
	private := NewGuard(true, true, nil)
	allowlist := NewGuard(false, false, []string{"jelly.example.com", "*.example.net"})

	tests := []struct {
		name    string
		guard   *Guard
		raw     string
		wantErr bool
	}{
		{"public https", public, "https://jelly.example.com", false},
		{"public https with path", public, "https://jelly.example.com/jellyfin/", false},
		{"trailing slash trimmed", public, "https://jelly.example.com/", false},
		{"plain http rejected", public, "http://jelly.example.com", true},
		{"plain http allowed when opted in", private, "http://jelly.example.com:8096", false},
		{"non http scheme", public, "ftp://jelly.example.com", true},
		{"credentials in URL", public, "https://user:pass@jelly.example.com", true},
		{"query rejected", public, "https://jelly.example.com?x=1", true},
		{"fragment rejected", public, "https://jelly.example.com#x", true},
		{"empty", public, "  ", true},
		{"garbage", public, "://nope", true},
		{"non standard port", public, "https://jelly.example.com:9999", true},
		{"standard port", public, "https://jelly.example.com:443", false},
		{"alt https port", public, "https://jelly.example.com:8443", false},
		{"allowlisted exact", allowlist, "https://jelly.example.com", false},
		{"allowlisted wildcard", allowlist, "https://media.example.net", false},
		{"not allowlisted", allowlist, "https://other.example.org", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.guard.ValidateURL(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateURL(%q) err = %v, wantErr = %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestValidateURLNormalizes(t *testing.T) {
	g := NewGuard(false, false, nil)
	u, err := g.ValidateURL("HTTPS://Jelly.Example.COM/JellyFin/")
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "https://jelly.example.com/JellyFin" {
		t.Errorf("normalized = %q", u.String())
	}
}

func TestAddressPolicy(t *testing.T) {
	public := NewGuard(false, false, nil)
	private := NewGuard(true, true, nil)

	tests := []struct {
		name    string
		guard   *Guard
		ip      string
		wantErr bool
	}{
		{"loopback public instance", public, "127.0.0.1", true},
		{"loopback when private allowed", private, "127.0.0.1", false},
		{"metadata endpoint always blocked", private, "169.254.169.254", true},
		{"private when allowed", private, "10.0.0.5", false},
		{"private when not allowed", public, "192.168.1.10", true},
		{"cgnat blocked", public, "100.64.0.1", true},
		{"unspecified", public, "0.0.0.0", true},
		{"ipv6 loopback", public, "::1", true},
		{"ipv6 ula", public, "fd00::1", true},
		{"ipv6 link-local", private, "fe80::1", true},
		{"public address", public, "1.1.1.1", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.guard.checkAddr(netip.MustParseAddr(tc.ip))
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkAddr(%s) err = %v, wantErr = %v", tc.ip, err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("err = %v, want ErrBlockedAddress", err)
			}
		})
	}
}

func TestDialRejectsBlockedHostWithoutConnecting(t *testing.T) {
	_, err := NewGuard(false, false, nil).DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
}
