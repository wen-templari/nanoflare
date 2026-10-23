package egress

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyResolutionRoutes(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			enabled   bool
			noProxy   []string
			allow     []string
			host      string
			ips       []string
			lookupErr error
			want      string
		}{
			{name: "delegated", enabled: true, want: "example.com:443"},
			{name: "disabled"},
			{name: "dns_timeout", enabled: true, lookupErr: &net.DNSError{Err: "timeout", IsTimeout: true, UnwrapErr: context.DeadlineExceeded}, want: "example.com:443"},
			{name: "empty_answers", enabled: true, ips: []string{}},
			{name: "no_proxy", enabled: true, noProxy: []string{"example.com"}},
			{name: "cidr_only", enabled: true, allow: []string{"8.8.8.0/24"}},
			{name: "private", enabled: true, ips: []string{"127.0.0.1"}},
			{name: "mixed", enabled: true, ips: []string{"8.8.8.8", "127.0.0.1"}},
			{name: "literal", enabled: true, host: "169.254.169.254"},
			{name: "pinned", enabled: true, ips: []string{"8.8.8.8"}, want: "8.8.8.8:443"},
			{name: "canceled", enabled: true, lookupErr: context.Canceled},
			{name: "deadline", enabled: true, lookupErr: context.DeadlineExceeded},
			{name: "malformed", enabled: true, host: "bad_host.example"},
		} {
			t.Run(fmt.Sprintf("raw=%t/%s", raw, tc.name), func(t *testing.T) {
				var logs bytes.Buffer
				old := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
				defer slog.SetDefault(old)
				var calls atomic.Int32
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Host != "example.com" {
						t.Errorf("origin Host: %s", r.Host)
					}
					fmt.Fprint(w, "proxy-response")
				}))
				defer origin.Close()
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if raw {
						if r.Method != "CONNECT" || r.Host != tc.want {
							t.Errorf("CONNECT target: %s %s", r.Method, r.Host)
						}
					} else if r.Method == "CONNECT" {
						if r.Host != "8.8.8.8:80" || tc.name != "pinned" {
							t.Errorf("unexpected HTTP tunnel: %s", r.Host)
						}
						tunnelTestHTTP(t, w, r, origin.Listener.Addr().String())
						return
					} else {
						want := strings.TrimSuffix(tc.want, ":443")
						if r.URL.Host != want {
							t.Errorf("absolute URL/Host: %s / %s, want %s", r.URL.Host, r.Host, want)
						}
					}
					w.WriteHeader(200)
					fmt.Fprint(w, "proxy-response")
				}))
				defer proxy.Close()
				allow := tc.allow
				if allow == nil {
					allow = []string{"public"}
				}
				adapter, err := New(Config{ProxyURL: proxy.URL, ProxyResolves: tc.enabled, NoProxy: tc.noProxy, NetworkAllow: allow, Addr: "127.0.0.1:0"})
				if err != nil {
					t.Fatal(err)
				}
				adapter.policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
					if tc.lookupErr != nil {
						return nil, tc.lookupErr
					}
					if tc.ips == nil {
						return nil, &net.DNSError{Err: "unavailable", Name: "example.com", IsNotFound: true}
					}
					var ips []net.IPAddr
					for _, ip := range tc.ips {
						ips = append(ips, net.IPAddr{IP: net.ParseIP(ip)})
					}
					return ips, nil
				}
				host := tc.host
				if host == "" {
					host = "example.com"
				}
				var response *http.Response
				if raw {
					if err = adapter.Start(); err != nil {
						t.Fatal(err)
					}
					defer closeAdapter(t, adapter)
					conn, err := net.Dial("tcp", adapter.Addr())
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(3 * time.Second))
					fmt.Fprintf(conn, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", host, host)
					response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					request, _ := http.NewRequest("GET", "http://"+host+"/secret?token=private", nil)
					request.Body = http.NoBody
					request.Header.Set("Authorization", "secret-header")
					response = adapter.forward(request)
				}
				defer response.Body.Close()
				if tc.want != "" {
					if response.StatusCode != 200 || calls.Load() != 1 {
						t.Fatalf("status %d, proxy calls %d", response.StatusCode, calls.Load())
					}
					if !raw {
						body, _ := io.ReadAll(response.Body)
						if string(body) != "proxy-response" {
							t.Fatalf("body %q", body)
						}
					}
				} else if response.StatusCode == 403 && tc.lookupErr == nil {
					route := "http_"
					if raw {
						route = "connect_"
					}
					if !strings.Contains(logs.String(), route) || !strings.Contains(logs.String(), "reason") {
						t.Errorf("missing denial diagnostics: %s", &logs)
					}
					if calls.Load() != 0 {
						t.Error("denial contacted proxy")
					}
					for _, secret := range []string{"secret?", "private", "secret-header"} {
						if strings.Contains(logs.String(), secret) {
							t.Errorf("log leaked request data: %s", &logs)
						}
					}
				} else if calls.Load() != 0 || response.StatusCode == 200 {
					t.Fatalf("denial: status %d, proxy calls %d", response.StatusCode, calls.Load())
				}
			})
		}
	}
}

func TestProxyResolutionRequiresSelectedProxy(t *testing.T) {
	rt, err := NewHTTPTransport(Config{ProxyResolves: true})
	if err != nil {
		t.Fatal(err)
	}
	transport := rt.(*policyTransport)
	transport.policy.lookup = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("offline") }
	request, _ := http.NewRequest("GET", "http://example.com/", nil)
	if _, err = rt.RoundTrip(request); !errors.Is(err, ErrDestinationDenied) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport.policy.lookup = func(context.Context, string) ([]net.IPAddr, error) { cancel(); return nil, errors.New("offline") }
	request = request.WithContext(ctx)
	if _, err = rt.RoundTrip(request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDenialDiagnosticsDoNotLeakRequestData(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	rt, _ := NewHTTPTransport(Config{})
	request, _ := http.NewRequest("GET", "http://169.254.169.254/private-path?secret-query", strings.NewReader("secret-body"))
	request.Header.Set("Authorization", "secret-header")
	_, err := rt.RoundTrip(request)
	if !errors.Is(err, ErrDestinationDenied) {
		t.Fatal(err)
	}
	for _, want := range []string{"169.254.169.254", "\"port\":\"80\"", "http_direct", "address_not_allowed"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %s: %s", want, &logs)
		}
	}
	for _, secret := range []string{"private-path", "secret-query", "secret-body", "secret-header"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log leaked %s", secret)
		}
	}
}

// A forwarding proxy may derive Host from the absolute URL. Verify identity
// at the origin after proxy parsing, for both pinned tunnels and delegated URLs.
func TestPolicyHTTPProxyPreservesOriginIdentity(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enabled, resolved bool
	}{
		{"default_pinned", false, true}, {"opt_in_pinned", true, true}, {"delegated", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var originCalls, proxyCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originCalls.Add(1)
				if r.Host != "customer.example:8080" {
					http.Error(w, "wrong virtual host", 421)
					return
				}
				if r.RequestURI != "/a%2Fb?q=one%20two" || r.Header.Get("Proxy-Authorization") != "" {
					t.Errorf("origin request: %s, headers %v", r.RequestURI, r.Header)
				}
				fmt.Fprint(w, "virtual-host-ok")
			}))
			defer origin.Close()
			base := http.DefaultTransport.(*http.Transport).Clone()
			base.Proxy = nil
			defer base.CloseIdleConnections()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyCalls.Add(1)
				if r.Header.Get("Proxy-Authorization") == "" {
					t.Error("missing proxy authentication")
				}
				if tc.resolved {
					if r.Method != "CONNECT" || r.Host != "8.8.8.8:8080" {
						t.Errorf("unpinned tunnel: %s %s", r.Method, r.Host)
						http.Error(w, "bad target", 400)
						return
					}
					tunnelTestHTTP(t, w, r, origin.Listener.Addr().String())
					return
				}
				if r.Method != "GET" || r.URL.Host != "customer.example:8080" {
					t.Errorf("delegated absolute URL: %s %s", r.Method, r.URL)
				}
				out := r.Clone(r.Context())
				out.RequestURI = ""
				out.URL.Host = origin.Listener.Addr().String()
				out.Header.Del("Proxy-Authorization")
				response, err := base.RoundTrip(out)
				if err != nil {
					t.Error(err)
					http.Error(w, "forward failed", 502)
					return
				}
				defer response.Body.Close()
				w.WriteHeader(response.StatusCode)
				io.Copy(w, response.Body)
			}))
			defer proxy.Close()
			proxyURL, _ := url.Parse(proxy.URL)
			proxyURL.User = url.UserPassword("operator", "proxy-secret")
			rt, err := NewHTTPTransport(Config{ProxyURL: proxyURL.String(), ProxyResolves: tc.enabled})
			if err != nil {
				t.Fatal(err)
			}
			rt.(*policyTransport).policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				if !tc.resolved {
					return nil, &net.DNSError{Err: "offline", IsNotFound: true}
				}
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
			}
			request, _ := http.NewRequest("GET", "http://customer.example:8080/a%2Fb?q=one%20two", nil)
			response, err := rt.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != 200 || string(body) != "virtual-host-ok" || proxyCalls.Load() != 1 || originCalls.Load() != 1 {
				t.Fatalf("status %d body %q proxy=%d origin=%d", response.StatusCode, body, proxyCalls.Load(), originCalls.Load())
			}
		})
	}
}

func tunnelTestHTTP(t *testing.T, w http.ResponseWriter, r *http.Request, target string) {
	t.Helper()
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		t.Error(err)
		http.Error(w, "dial failed", 502)
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
}

func TestPinnedHTTPProxyRefusalDoesNotFallback(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "CONNECT" || r.Host != "8.8.8.8:80" {
			t.Errorf("unexpected fallback: %s %s", r.Method, r.Host)
		}
		http.Error(w, "CONNECT disallowed", 403)
	}))
	defer proxy.Close()
	rt, err := NewHTTPTransport(Config{ProxyURL: proxy.URL, ProxyResolves: true})
	if err != nil {
		t.Fatal(err)
	}
	rt.(*policyTransport).policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	request, _ := http.NewRequest("GET", "http://customer.example/", nil)
	if response, err := rt.RoundTrip(request); err == nil {
		response.Body.Close()
		t.Fatal("proxy refusal succeeded")
	}
	if calls.Load() != 1 {
		t.Fatalf("proxy requests: %d", calls.Load())
	}
}

func TestPinnedHTTPProxyHandshakeCancellation(t *testing.T) {
	accepted := make(chan struct{})
	disconnected := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, reader, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		close(accepted)
		_, _ = reader.ReadByte() // no CONNECT response; wait for client cancellation
		close(disconnected)
	}))
	defer proxy.Close()
	rt, err := NewHTTPTransport(Config{ProxyURL: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	rt.(*policyTransport).policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://customer.example/", nil)
	done := make(chan error, 1)
	go func() {
		response, err := rt.RoundTrip(request)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy never received CONNECT")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request stuck in CONNECT handshake")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("cancellation left CONNECT connection open")
	}
}
