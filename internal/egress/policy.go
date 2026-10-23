package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrDestinationDenied = errors.New("outbound destination is unavailable under the network policy")

// destinationDenial keeps diagnostics bounded and never includes resolver errors.
type destinationDenial struct{ reason string }

func (e *destinationDenial) Error() string { return ErrDestinationDenied.Error() }
func (e *destinationDenial) Unwrap() error { return ErrDestinationDenied }
func denied(reason string) error           { return &destinationDenial{reason: reason} }

func logDenial(host, port, route string, err error) {
	var denial *destinationDenial
	if !validHostname(host) && net.ParseIP(host) == nil {
		host = ""
	}
	if len(port) > 5 || strings.IndexFunc(port, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
		port = ""
	}
	if errors.As(err, &denial) {
		slog.Warn("outbound destination denied", "hostname", host, "port", port, "route", route, "reason", denial.reason)
	}
}

// validHostname excludes malformed authorities and ambiguous IP spellings.
func validHostname(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	numeric := true
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
			if c < '0' || c > '9' {
				numeric = false
			}
		}
	}
	return !numeric
}

// resolveRoute delegates only failed DNS lookups to an explicitly trusted proxy.
func (p *NetworkPolicy) resolveRoute(ctx context.Context, host, port string, proxy *url.URL, proxyResolves bool) (string, error) {
	target, err := p.resolve(ctx, host, port)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var denial *destinationDenial
	if proxyResolves && proxy != nil && p.public && errors.As(err, &denial) && denial.reason == "dns_lookup_failed" {
		return net.JoinHostPort(host, port), nil
	}
	return target, err
}

// NetworkPolicy authorizes resolved addresses independently of proxy routing.
// Explicit administrator CIDRs may permit private destinations; NO_PROXY cannot.
type NetworkPolicy struct {
	prefixes               []netip.Prefix
	public, private, local bool
	lookup                 func(context.Context, string) ([]net.IPAddr, error)
}

func NewNetworkPolicy(allow []string) (*NetworkPolicy, error) {
	p := &NetworkPolicy{lookup: net.DefaultResolver.LookupIPAddr}
	for _, rule := range allow {
		rule = strings.TrimSpace(rule)
		switch rule {
		case "public":
			p.public = true
		case "private":
			p.private = true
		case "local":
			p.local = true
		case "":
		default:
			prefix, err := netip.ParsePrefix(rule)
			if err != nil {
				addr, e := netip.ParseAddr(rule)
				if e != nil {
					return nil, errors.New("unsupported outbound network allow rule")
				}
				addr = addr.Unmap()
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			}
			p.prefixes = append(p.prefixes, prefix.Masked())
		}
	}
	return p, nil
}

var nonPublicNetworks = func() []netip.Prefix {
	var out []netip.Prefix
	for _, value := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:2::/48", "2001:10::/28", "2001:20::/28", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(value))
	}
	return out
}()

func (p *NetworkPolicy) allows(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" || addr.IsUnspecified() || addr.IsMulticast() {
		return false
	}
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	if addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return p.local
	}
	if addr.IsPrivate() {
		return p.private
	}
	for _, prefix := range nonPublicNetworks {
		if prefix.Contains(addr) {
			return false
		}
	}
	return p.public && addr.IsGlobalUnicast()
}
func (p *NetworkPolicy) resolve(ctx context.Context, host, port string) (string, error) {
	number, err := strconv.Atoi(port)
	if err != nil || strings.IndexFunc(port, func(c rune) bool { return c < '0' || c > '9' }) >= 0 || number < 1 || number > 65535 || host == "" || strings.Contains(host, "%") {
		return "", denied("malformed_destination")
	}
	var addresses []net.IPAddr
	if addr, err := netip.ParseAddr(host); err == nil {
		addresses = []net.IPAddr{{IP: net.IP(addr.AsSlice())}}
	} else {
		if !validHostname(host) {
			return "", denied("malformed_destination")
		}
		lookupContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		addresses, err = p.lookup(lookupContext, host)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// A resolver timeout is a DNS failure when the caller is still
			// active. Caller cancellation/deadlines never delegate resolution.
			var dnsError *net.DNSError
			dnsTimeout := errors.As(err, &dnsError) && dnsError.IsTimeout
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && !dnsTimeout {
				return "", err
			}
			return "", denied("dns_lookup_failed")
		}
	}
	if len(addresses) == 0 || len(addresses) > 512 {
		return "", denied("invalid_dns_answer")
	}
	// Reject mixed public/private answers rather than letting a later resolver or
	// retry choose an address which was never authorized.
	for _, address := range addresses {
		addr, ok := netip.AddrFromSlice(address.IP)
		if !ok || address.Zone != "" || !p.allows(addr) {
			return "", denied("address_not_allowed")
		}
	}
	return net.JoinHostPort(addresses[0].IP.String(), port), nil
}

// policyTransport resolves each request once, validates every answer, and sends
// the authorized literal IP to direct dials and proxy CONNECT tunnels.
// Explicit proxy resolution delegation applies only after a failed DNS lookup.
// Original Host and TLS server name are preserved. Connections are not pooled
// across requests; the pilot's latency/capacity qualification must include this.
type policyTransport struct {
	policy        *NetworkPolicy
	base          *http.Transport
	slots         chan struct{}
	proxyResolves bool
}

func NewPolicyTransport(allow []string, base *http.Transport) (http.RoundTripper, error) {
	policy, err := NewNetworkPolicy(allow)
	if err != nil {
		return nil, err
	}
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = nil
	}
	return &policyTransport{policy: policy, base: base, slots: make(chan struct{}, 64)}, nil
}
func (p *policyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil || request.URL.User != nil || request.URL.Opaque != "" || request.URL.Fragment != "" || (request.URL.Scheme != "http" && request.URL.Scheme != "https") || request.Method == http.MethodConnect {
		err := denied("malformed_destination")
		logDenial("", "", "http", err)
		return nil, err
	}
	select {
	case p.slots <- struct{}{}:
	case <-request.Context().Done():
		return nil, request.Context().Err()
	}
	held := false
	release := func() { <-p.slots }
	defer func() {
		if !held {
			release()
		}
	}()
	port := request.URL.Port()
	if port == "" {
		port = "80"
		if request.URL.Scheme == "https" {
			port = "443"
		}
	}
	var proxy *url.URL
	var err error
	if p.base.Proxy != nil {
		proxy, err = p.base.Proxy(request)
		if err != nil {
			return nil, err
		}
	}
	pinned, err := p.policy.resolveRoute(request.Context(), request.URL.Hostname(), port, proxy, p.proxyResolves)
	if err != nil {
		route := "http_direct"
		if proxy != nil {
			route = "http_proxy"
		}
		logDenial(request.URL.Hostname(), port, route, err)
		return nil, err
	}
	transport := p.base.Clone()
	transport.Proxy = http.ProxyURL(proxy)
	transport.DisableKeepAlives = true
	transport.TLSClientConfig = p.base.TLSClientConfig.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	transport.TLSClientConfig.ServerName = request.URL.Hostname()
	out := request.Clone(request.Context())
	out.URL.Host = pinned
	if proxy != nil && out.URL.Scheme == "http" {
		host, _, _ := net.SplitHostPort(pinned)
		if net.ParseIP(host) != nil {
			// Absolute URLs with a pinned IP let proxies replace the origin Host.
			// Tunnel to that IP and send origin-form HTTP with the original Host.
			// A proxy which refuses CONNECT must fail closed; never re-resolve.
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				return dialProxyTunnel(ctx, proxy, pinned, p.base.DialContext, proxyConnectTimeout)
			}
		}
	}

	out.Host = request.URL.Host
	out.RequestURI = ""
	out.Header = out.Header.Clone()
	removeHopHeaders(out.Header)
	response, err := transport.RoundTrip(out)
	transport.CloseIdleConnections()
	if response != nil {
		removeHopHeaders(response.Header)
		if err == nil {
			response.Body = &policyResponseBody{ReadCloser: response.Body, release: release}
			held = true
		}
	}
	return response, err
}

// NewHTTPTransport configures an authorized HTTP client, with optional corporate
// routing. Proxy credentials and CA files stay in the trusted Go service.
func NewHTTPTransport(config Config) (http.RoundTripper, error) {
	roots, err := loadRoots(config.CAFiles)
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	base.ResponseHeaderTimeout = 10 * time.Second
	base.TLSHandshakeTimeout = 10 * time.Second
	if strings.TrimSpace(config.ProxyURL) != "" {
		proxy, err := parseProxyURL(config.ProxyURL)
		if err != nil {
			return nil, err
		}
		base.Proxy = newProxySelector(proxy, config.NoProxy)
	}
	allow := config.NetworkAllow
	if allow == nil {
		allow = []string{"public"}
	}
	rt, err := NewPolicyTransport(allow, base)
	if err != nil {
		return nil, err
	}
	rt.(*policyTransport).proxyResolves = config.ProxyResolves
	return rt, nil
}

type policyResponseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *policyResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}
