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
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if raw {
						if r.Method != "CONNECT" || r.Host != tc.want {
							t.Errorf("CONNECT target: %s %s", r.Method, r.Host)
						}
					} else {
						want := strings.TrimSuffix(tc.want, ":443") + ":80"
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

// Read the wire directly: net/http's server parser replaces Host with the
// absolute URL's authority and would hide an accidental Host-header rewrite.
func TestPolicyHTTPProxyPreservesWireIdentity(t *testing.T) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	wire := make(chan string, 1)
	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			wire <- "accept failed"
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		var head strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				wire <- "read failed"
				return
			}
			head.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		wire <- head.String()
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
	}()
	rt, err := NewHTTPTransport(Config{ProxyURL: "http://" + proxy.Addr().String(), ProxyResolves: true})
	if err != nil {
		t.Fatal(err)
	}
	rt.(*policyTransport).policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	request, _ := http.NewRequest("GET", "http://example.com/a%2Fb?q=one%20two", nil)
	response, err := rt.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	head := <-wire
	for _, want := range []string{"GET http://8.8.8.8:80/a%2Fb?q=one%20two HTTP/1.1\r\n", "Host: example.com\r\n"} {
		if !strings.Contains(head, want) {
			t.Errorf("missing %q in %q", want, head)
		}
	}
}
