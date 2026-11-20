package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ErrBlockedAddress is returned when a Jellyfin URL resolves to an address the
// server refuses to contact. This is the SSRF guard: the URL is user supplied
// on a public deployment, so loopback, link-local (cloud metadata) and private
// ranges are off limits unless the operator opts in.
var ErrBlockedAddress = errors.New("jellyfin: blocked address")

var (
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
)

// Guard validates and dials Jellyfin servers. Every outbound connection goes
// through DialContext, which resolves the host, validates the resolved
// addresses and dials the validated IP directly. Resolving inside the dialer is
// deliberate: it closes the DNS-rebinding window between validation and
// connection.
type Guard struct {
	allowPrivate  bool
	allowInsecure bool
	allowedHosts  []string

	resolver *net.Resolver
	dialer   net.Dialer
}

// NewGuard builds a Guard. allowPrivate permits RFC1918/loopback/CGNAT targets
// (self-hosted deployments); allowInsecure permits plain HTTP and non-standard
// ports; allowedHosts, when non-empty, restricts targets to those host names.
func NewGuard(allowPrivate, allowInsecure bool, allowedHosts []string) *Guard {
	hosts := make([]string, 0, len(allowedHosts))
	for _, h := range allowedHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			hosts = append(hosts, h)
		}
	}
	return &Guard{
		allowPrivate:  allowPrivate,
		allowInsecure: allowInsecure,
		allowedHosts:  hosts,
		resolver:      net.DefaultResolver,
		dialer:        net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}
}

// ValidateURL parses and checks a user-supplied Jellyfin base URL.
func (g *Guard) ValidateURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("jellyfin: empty server URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: invalid server URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("jellyfin: server URL must use http or https")
	}
	if u.Scheme == "http" && !g.allowInsecure {
		return nil, errors.New("jellyfin: server URL must use https")
	}
	if u.User != nil {
		return nil, errors.New("jellyfin: server URL must not contain credentials")
	}
	if u.Host == "" {
		return nil, errors.New("jellyfin: server URL is missing a host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("jellyfin: server URL must not contain a query or fragment")
	}
	if !g.hostAllowed(u.Hostname()) {
		return nil, fmt.Errorf("jellyfin: host %q is not allowed", u.Hostname())
	}
	if !g.allowInsecure && !allowedPort(u.Port()) {
		return nil, fmt.Errorf("jellyfin: port %q is not allowed", u.Port())
	}

	u.Path = strings.TrimRight(u.Path, "/")
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u, nil
}

func (g *Guard) hostAllowed(host string) bool {
	if len(g.allowedHosts) == 0 {
		return true
	}
	host = strings.ToLower(host)
	for _, allowed := range g.allowedHosts {
		if host == allowed || strings.HasSuffix(host, "."+strings.TrimPrefix(allowed, "*.")) {
			return true
		}
	}
	return false
}

func allowedPort(port string) bool {
	switch port {
	case "", "443", "8443", "8920":
		return true
	}
	return false
}

// DialContext resolves addr, validates every candidate address and dials one of
// the validated IPs. Hostnames are never handed to the OS resolver again, so a
// rebinding record cannot redirect the connection after the check.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range addrs {
		if err := g.checkAddr(ip); err != nil {
			return nil, err
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: %s has no addresses", ErrBlockedAddress, host)
	}

	var lastErr error
	for _, ip := range addrs {
		conn, err := g.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ips, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: resolve %s: %w", host, err)
	}
	return ips, nil
}

// checkAddr rejects addresses that must never be reachable from a public
// deployment. Link-local is always blocked (cloud metadata lives there), even
// when private ranges are allowed.
func (g *Guard) checkAddr(ip netip.Addr) error {
	if !ip.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlockedAddress)
	}
	ip = ip.Unmap()

	switch {
	case ip.IsUnspecified(), ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return fmt.Errorf("%w: link-local %s", ErrBlockedAddress, ip)
	}

	if !g.allowPrivate {
		switch {
		case ip.IsLoopback(), ip.IsPrivate(), cgnat.Contains(ip), thisNetwork.Contains(ip):
			return fmt.Errorf("%w: private %s", ErrBlockedAddress, ip)
		}
	}
	return nil
}

// HTTPClient returns a client whose only transport is this guard.
func (g *Guard) HTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:               nil, // never route user URLs through an environment proxy
		DialContext:         g.DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("jellyfin: too many redirects")
			}
			_, err := g.ValidateURL(req.URL.String())
			return err
		},
	}
}
