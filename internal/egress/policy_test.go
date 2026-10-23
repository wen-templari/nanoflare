package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkPolicyAddressMatrix(t *testing.T) {
	public, _ := NewNetworkPolicy([]string{"public"})
	for _, value := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "fe80::1", "fc00::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "100.64.0.1", "192.0.2.1", "198.18.0.1", "240.0.0.1", "64:ff9b::7f00:1", "::ffff:127.0.0.1", "2001:db8::1", "2002:7f00:1::"} {
		if public.allows(netip.MustParseAddr(value)) {
			t.Errorf("public allowed %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !public.allows(netip.MustParseAddr(value)) {
			t.Errorf("public denied %s", value)
		}
	}
	explicit, _ := NewNetworkPolicy([]string{"10.0.0.0/8", "127.0.0.1/32"})
	if !explicit.allows(netip.MustParseAddr("10.1.1.1")) || !explicit.allows(netip.MustParseAddr("127.0.0.1")) || explicit.allows(netip.MustParseAddr("192.168.1.1")) {
		t.Fatal("explicit network authorization mismatch")
	}
	if _, err := NewNetworkPolicy([]string{"public,local"}); err == nil {
		t.Fatal("accepted unparsed network rule")
	}
}
func TestNetworkPolicyPinsResolvedAddressAndRejectsRebinding(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "customer.example" {
			t.Errorf("Host lost: %s", r.Host)
		}
		fmt.Fprint(w, "authorized")
	}))
	defer target.Close()
	var dials, lookups atomic.Int32
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if address != "8.8.8.8:80" {
			t.Errorf("unapproved dial: %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, target.Listener.Addr().String())
	}
	rt, _ := NewPolicyTransport([]string{"public"}, base)
	policy := rt.(*policyTransport).policy
	policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		if lookups.Add(1) == 1 {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	client := &http.Client{Transport: rt}
	response, err := client.Get("http://customer.example/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "authorized" {
		t.Fatal(string(body))
	}
	if _, err = client.Get("http://customer.example/"); !errors.Is(err, ErrDestinationDenied) {
		t.Fatalf("DNS rebinding: %v", err)
	}
	if dials.Load() != 1 {
		t.Fatal("denied request dialed a target")
	}
	policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("::1")}}, nil
	}
	if _, err = client.Get("http://customer.example/"); !errors.Is(err, ErrDestinationDenied) {
		t.Fatalf("mixed addresses: %v", err)
	}
}
func TestNetworkPolicyChecksRedirectAndNoProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", 302)
	}))
	defer target.Close()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil } // NO_PROXY is routing only.
	base.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, target.Listener.Addr().String())
	}
	rt, _ := NewPolicyTransport([]string{"public"}, base)
	rt.(*policyTransport).policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	if _, err := (&http.Client{Transport: rt}).Get("http://redirect.example/"); !errors.Is(err, ErrDestinationDenied) {
		t.Fatalf("redirect metadata bypass: %v", err)
	}
	if _, err := (&http.Client{Transport: rt}).Get(target.URL); !errors.Is(err, ErrDestinationDenied) {
		t.Fatalf("NO_PROXY bypass: %v", err)
	}
}
func TestPolicyProxyUsesPinnedIPAndPreservesTLSIdentity(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(fmt.Sprintf("delegated=%t", delegated), func(t *testing.T) {
			expected := "8.8.8.8:443"
			if delegated {
				expected = "example.com:443"
			}
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "example.com" || r.Header.Get("Proxy-Authorization") != "" {
					t.Errorf("origin identity or proxy credential leak: %s", r.Host)
				}
				fmt.Fprint(w, "verified-tls")
			}))
			defer target.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Host != expected || !strings.HasPrefix(r.Header.Get("Proxy-Authorization"), "Basic ") {
					t.Errorf("un-pinned proxy request: %s %s", r.Method, r.Host)
					http.Error(w, "invalid", 400)
					return
				}
				upstream, err := net.Dial("tcp", target.Listener.Addr().String())
				if err != nil {
					t.Error(err)
					return
				}
				defer upstream.Close()
				client, reader, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer client.Close()
				fmt.Fprint(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
				done := make(chan struct{})
				go func() { io.Copy(upstream, reader); upstream.Close(); close(done) }()
				io.Copy(client, upstream)
				client.Close()
				<-done
			}))
			defer proxy.Close()
			proxyURL, _ := url.Parse(proxy.URL)
			proxyURL.User = url.UserPassword("operator", "secret")
			roots := x509.NewCertPool()
			roots.AddCert(target.Certificate())
			base := http.DefaultTransport.(*http.Transport).Clone()
			base.Proxy = http.ProxyURL(proxyURL)
			base.TLSClientConfig = &tls.Config{RootCAs: roots}
			rt, _ := NewPolicyTransport([]string{"public"}, base)
			rt.(*policyTransport).proxyResolves = delegated
			rt.(*policyTransport).policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				if delegated {
					return nil, &net.DNSError{Err: "offline", IsNotFound: true}
				}
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
			}
			response, err := (&http.Client{Transport: rt}).Get("https://example.com/")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) != "verified-tls" {
				t.Fatal(string(body))
			}
		})
	}
}
func TestConfiguredAdapterDeniesUnapprovedRawTCPEvenNoProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("denied CONNECT reached corporate proxy") }))
	defer proxy.Close()
	adapter, err := New(Config{ProxyURL: proxy.URL, Addr: "127.0.0.1:0", NoProxy: []string{"*"}, NetworkAllow: []string{"public"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeAdapter(t, adapter)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "CONNECT 127.0.0.1:22 HTTP/1.1\r\nHost: 127.0.0.1:22\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("rawTCP: %d", response.StatusCode)
	}
}

func TestPolicyHTTPAdmissionWaitHonorsCancellation(t *testing.T) {
	roundTripper, err := NewPolicyTransport([]string{"public"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripper.(*policyTransport)
	for range cap(transport.slots) {
		transport.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://8.8.8.8/", nil)
	if _, err = transport.RoundTrip(request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission ignored caller lifetime: %v", err)
	}
	for range cap(transport.slots) {
		<-transport.slots
	}
	request, _ = http.NewRequest("GET", "http://127.0.0.1/", nil)
	if _, err = transport.RoundTrip(request); !errors.Is(err, ErrDestinationDenied) {
		t.Fatal(err)
	}
	if len(transport.slots) != 0 {
		t.Fatal("denied request leaked admission capacity")
	}
}
