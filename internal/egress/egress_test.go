package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProxyURLValidation(t *testing.T) {
	for _, value := range []string{"", "https://proxy.example:8080", "http:///missing", "http://proxy.example/path"} {
		t.Run(value, func(t *testing.T) {
			if _, err := New(Config{ProxyURL: value}); err == nil {
				t.Fatalf("New(%q) succeeded", value)
			}
		})
	}
	if _, err := New(Config{ProxyURL: "http://user:secret@proxy.example:8080"}); err != nil {
		t.Fatalf("valid proxy URL rejected: %v", err)
	}
}

func TestNoProxyRules(t *testing.T) {
	proxyURL, err := url.Parse("http://proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	selector := newProxySelector(proxyURL, []string{"10.0.0.0/8", ".corp.example", "localhost"})
	for _, test := range []struct {
		url       string
		wantProxy bool
	}{
		{"http://10.12.0.1/", false},
		{"https://api.corp.example/", false},
		{"http://localhost:8080/", false},
		{"https://example.com/", true},
	} {
		requestURL, err := url.Parse(test.url)
		if err != nil {
			t.Fatal(err)
		}
		got, err := selector(&http.Request{URL: requestURL})
		if err != nil {
			t.Fatal(err)
		}
		if (got != nil) != test.wantProxy {
			t.Errorf("proxy for %s = %v, want proxy=%v", test.url, got, test.wantProxy)
		}
	}
}

func TestAdapterForwardsAbsoluteHTTPRequestThroughConfiguredProxy(t *testing.T) {
	requests := make(chan *http.Request, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		w.Header().Set("X-Origin", "yes")
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxy.Close()
	adapter := startAdapter(t, proxy.URL)
	defer closeAdapter(t, adapter)
	adapterURL, _ := url.Parse("http://" + adapter.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(adapterURL)}}
	response, err := client.Get("http://public.example/stream")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Header.Get("X-Origin") != "yes" {
		t.Fatalf("response did not come from origin: %#v", response.Header)
	}
	select {
	case got := <-requests:
		if got.URL.String() != "http://public.example/stream" {
			t.Fatalf("proxy URL = %q", got.URL)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy received no request")
	}
}

func TestAdapterAcceptsHostlessAbsoluteHTTPRequest(t *testing.T) {
	targetURL := ""
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want, _ := url.Parse(targetURL)
		if r.Host != want.Host {
			t.Errorf("origin Host = %q, want %q", r.Host, want.Host)
		}
		_, _ = w.Write([]byte("hostless request forwarded"))
	}))
	defer target.Close()
	targetURL = target.URL
	proxy := newForwardingProxy(t)
	defer proxy.Close()
	adapter := startAdapter(t, proxy.URL)
	defer closeAdapter(t, adapter)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET %s/worker HTTP/1.1\r\nConnection: close\r\n\r\n", target.URL); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "hostless request forwarded" {
		t.Fatalf("response = %s %q", response.Status, body)
	}
}

func TestAdapterHandlesTwoRequestsOnOneConnection(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "response for %s", r.URL.Path)
	}))
	defer target.Close()
	proxy := newForwardingProxy(t)
	defer proxy.Close()
	adapter := startAdapter(t, proxy.URL)
	defer closeAdapter(t, adapter)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for index, path := range []string{"/first", "/second"} {
		connection := ""
		if index == 1 {
			connection = "Connection: close\r\n"
		}
		if _, err := fmt.Fprintf(conn, "GET %s%s HTTP/1.1\r\n%s\r\n", target.URL, path, connection); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(body), "response for "+path; got != want {
			t.Fatalf("body = %q, want %q", got, want)
		}
	}
}

func TestAdapterDrainsRequestBodyBeforeReusingConnection(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reject" {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		_, _ = w.Write([]byte("second request"))
	}))
	defer proxy.Close()
	adapter := startAdapter(t, proxy.URL)
	defer closeAdapter(t, adapter)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := strings.Repeat("x", 512*1024)
	if _, err := fmt.Fprintf(conn, "POST http://origin.example/reject HTTP/1.1\r\nContent-Length: %d\r\n\r\n%sGET http://origin.example/next HTTP/1.1\r\nConnection: close\r\n\r\n", len(body), body); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	first, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("first response = %s, want 413", first.Status)
	}
	second, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := io.ReadAll(second.Body)
	second.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if second.StatusCode != http.StatusOK || string(secondBody) != "second request" {
		t.Fatalf("second response = %s %q", second.Status, secondBody)
	}
}

func TestAdapterIdleTimeoutClosesWithoutResponse(t *testing.T) {
	proxy := newForwardingProxy(t)
	defer proxy.Close()
	adapter, err := New(Config{ProxyURL: proxy.URL, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	adapter.readTimeout = 25 * time.Millisecond
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeAdapter(t, adapter)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := http.ReadResponse(bufio.NewReader(conn), nil); err == nil || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)) {
		t.Fatalf("idle connection read error = %v, want silent close", err)
	}
}

func TestAdapterPartialRequestTimeoutReturnsBadRequest(t *testing.T) {
	proxy := newForwardingProxy(t)
	defer proxy.Close()
	adapter, err := New(Config{ProxyURL: proxy.URL, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	adapter.readTimeout = 25 * time.Millisecond
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeAdapter(t, adapter)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET http://example.com/ HTTP/1.1\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("partial response = %s, want 400", response.Status)
	}
}

func TestAdapterCloseWaitsForInflightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("completed"))
	}))
	defer target.Close()
	proxy := newForwardingProxy(t)
	defer proxy.Close()
	adapter := startAdapter(t, proxy.URL)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nConnection: close\r\n\r\n", target.URL); err != nil {
		t.Fatal(err)
	}
	<-started
	closed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		closed <- adapter.Close(ctx)
	}()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before in-flight request completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "completed" {
		t.Fatalf("response body = %q, error = %v", body, err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestAdapterCloseImmediatelyClosesIdleKeepAliveConnection(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("completed"))
	}))
	defer target.Close()
	proxy := newForwardingProxy(t)
	defer proxy.Close()
	adapter := startAdapter(t, proxy.URL)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\n\r\n", target.URL); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := adapter.Close(ctx); err != nil {
		t.Fatalf("Close failed with an idle keep-alive connection: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("idle connection remained open after Close: %v", err)
	}
}

func TestAdapterRemovesConnectionNominatedHeaders(t *testing.T) {
	proxyHeaders := make(chan http.Header, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHeaders <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	adapter := startAdapter(t, proxy.URL)
	defer closeAdapter(t, adapter)
	adapterURL, _ := url.Parse("http://" + adapter.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(adapterURL)}}
	request, err := http.NewRequest(http.MethodGet, "http://origin.example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Connection", "X-Remove-Me")
	request.Header.Set("X-Remove-Me", "secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case header := <-proxyHeaders:
		if header.Get("Connection") != "" || header.Get("X-Remove-Me") != "" {
			t.Fatalf("proxy received hop-by-hop headers: %#v", header)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy received no request")
	}
}

func TestCloseCancelsStalledUpstreamRequest(t *testing.T) {
	started := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer proxy.Close()
	adapter := startAdapter(t, proxy.URL)
	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprint(conn, "GET http://origin.example/stalled HTTP/1.1\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := adapter.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want deadline exceeded", err)
	}
}

func TestRejectsNonLoopbackListener(t *testing.T) {
	adapter, err := New(Config{ProxyURL: "http://proxy.example", Addr: "0.0.0.0:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err == nil {
		t.Fatal("Start succeeded with a public listener")
	}
}

func TestAdapterTunnelsRawTCPConnectDirectlyForNoProxyTarget(t *testing.T) {
	echo := startEchoTCPServer(t)
	host, _, err := net.SplitHostPort(echo)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{ProxyURL: "http://127.0.0.1:9", NoProxy: []string{host}, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeAdapter(t, adapter)
	assertTunnelEcho(t, adapter.Addr(), echo)
}

func TestAdapterTunnelsRawTCPConnectThroughProxy(t *testing.T) {
	echo := startEchoTCPServer(t)
	adapter := startAdapter(t, "http://"+startConnectProxyServer(t))
	defer closeAdapter(t, adapter)
	assertTunnelEcho(t, adapter.Addr(), echo)
}

// TestConnectFailsFastWhenProxyStalls guards against the s3 hang: a proxy that
// accepts the TCP connection but never answers CONNECT (as corporate proxies do
// for non-443 ports like MQTT/PostgreSQL) must make the adapter return a prompt
// 502 rather than wedge the tunnel and hang the Worker forever.
func TestConnectFailsFastWhenProxyStalls(t *testing.T) {
	stalling := startStallingProxyServer(t)
	adapter, err := New(Config{ProxyURL: "http://" + stalling, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	adapter.connectTO = 300 * time.Millisecond // shrink the real 15s bound for the test
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeAdapter(t, adapter)

	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Generous relative to connectTO but far below any "hang": if the fix
	// regresses, this deadline trips instead of blocking indefinitely.
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "CONNECT db.internal:5432 HTTP/1.1\r\nHost: db.internal:5432\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("adapter did not respond to a stalled proxy CONNECT (hang regressed?): %v", err)
	}
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %s, want 502 Bad Gateway", response.Status)
	}
}

// startStallingProxyServer accepts connections and reads the CONNECT request but
// never sends any response, emulating a proxy that refuses a port silently.
func startStallingProxyServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				for {
					line, err := reader.ReadString('\n')
					if err != nil || line == "\r\n" {
						// Request read; deliberately never reply. Block until
						// the adapter gives up and closes its side (EOF here).
						_, _ = io.Copy(io.Discard, c)
						return
					}
				}
			}(conn)
		}
	}()
	return listener.Addr().String()
}

// TestIsDirectIPClassifiesRanges checks the reachability classifier that keeps
// corporate/private raw-TCP targets off the proxy path: loopback, RFC1918, and
// link-local are direct; public addresses are not, unless they fall inside a
// configured NO_PROXY CIDR (a corporate range that may be publicly routable).
func TestIsDirectIPClassifiesRanges(t *testing.T) {
	adapter, err := New(Config{ProxyURL: "http://127.0.0.1:9", NoProxy: []string{"203.0.113.0/24", ".corp"}, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		ip         string
		wantDirect bool
	}{
		{"127.0.0.1", true},             // loopback
		{"10.161.189.40", true},         // RFC1918 (the MQTT broker's class)
		{"192.168.1.10", true},          // RFC1918
		{"172.16.5.9", true},            // RFC1918
		{"169.254.10.1", true},          // link-local
		{"203.0.113.7", true},           // inside the configured corporate CIDR
		{"8.8.8.8", false},              // public
		{"203.0.114.1", false},          // just outside the configured CIDR
		{"::1", true},                   // IPv6 loopback
		{"fd00::1", true},               // IPv6 unique-local (private)
		{"2606:4700:4700::1111", false}, // public IPv6
	} {
		if got := adapter.isDirectIP(net.ParseIP(test.ip)); got != test.wantDirect {
			t.Errorf("isDirectIP(%s) = %v, want %v", test.ip, got, test.wantDirect)
		}
	}
}

// TestResolvesDirectRoutesRawTCPByResolvedAddress covers the wx0dnox0lc04 case:
// a bare hostname (not an IP literal, not listed in NO_PROXY) that resolves into
// private space must route direct-dial, not proxy. A public or partially-public
// resolution, or a lookup failure, stays on the proxy path.
func TestResolvesDirectRoutesRawTCPByResolvedAddress(t *testing.T) {
	adapter, err := New(Config{ProxyURL: "http://127.0.0.1:9", NoProxy: []string{"203.0.113.0/24"}, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	resolved := map[string][]net.IPAddr{
		"source-db":     {{IP: net.ParseIP("10.20.30.40")}},                                  // corporate, private
		"corp-host":     {{IP: net.ParseIP("203.0.113.9")}},                                  // corporate, in configured CIDR
		"public-db":     {{IP: net.ParseIP("93.184.216.34")}},                                // public
		"split-horizon": {{IP: net.ParseIP("10.0.0.5")}, {IP: net.ParseIP("93.184.216.34")}}, // mixed => proxy
	}
	adapter.lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if addrs, ok := resolved[host]; ok {
			return addrs, nil
		}
		return nil, fmt.Errorf("no such host %q", host)
	}
	for _, test := range []struct {
		target     string
		wantDirect bool
	}{
		{"10.20.30.40:5432", true},    // private IP literal
		{"93.184.216.34:5432", false}, // public IP literal
		{"source-db:5432", true},      // hostname -> private
		{"corp-host:5432", true},      // hostname -> configured corporate CIDR
		{"public-db:5432", false},     // hostname -> public
		{"split-horizon:5432", false}, // hostname -> mixed private+public
		{"unknown-host:5432", false},  // lookup failure -> proxy path (fail-fast applies)
	} {
		if got := adapter.resolvesDirect(context.Background(), test.target); got != test.wantDirect {
			t.Errorf("resolvesDirect(%s) = %v, want %v", test.target, got, test.wantDirect)
		}
	}
}

// assertTunnelEcho sends a CONNECT to the adapter, then verifies raw bytes flow
// end to end to the echo target behind it.
func assertTunnelEcho(t *testing.T, adapterAddr, target string) {
	t.Helper()
	conn, err := net.Dial("tcp", adapterAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %s, want 200", response.Status)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "pong" {
		t.Fatalf("tunnel echo = %q, want %q", buffer, "pong")
	}
}

// TestForwardFramesCloseDelimitedResponse guards the version-agnostic framing
// fix: an upstream response with no Content-Length and no chunked encoding
// (delimited only by connection close — valid HTTP/1.0 and HTTP/1.1, and the
// shape an HTTP/2 body also arrives in) must be re-framed as chunked when
// relayed over the keep-alive hop to workerd, or the client hangs waiting for a
// body end that never arrives.
func TestForwardFramesCloseDelimitedResponse(t *testing.T) {
	upstream := startCloseDelimitedHTTPServer(t, "hello world")
	host, _, err := net.SplitHostPort(upstream)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{ProxyURL: "http://127.0.0.1:9", NoProxy: []string{host}, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeAdapter(t, adapter)

	conn, err := net.Dial("tcp", adapter.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A deadline turns the pre-fix hang into a test failure instead of a stall.
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n", upstream, host); err != nil {
		t.Fatal(err)
	}

	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %s, want 200", response.Status)
	}
	if !hasChunkedEncoding(response.TransferEncoding) {
		t.Fatalf("adapter did not add framing: TransferEncoding=%v ContentLength=%d Close=%v",
			response.TransferEncoding, response.ContentLength, response.Close)
	}
	if response.Close {
		t.Fatalf("keep-alive hop was downgraded to connection-close")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading relayed body: %v", err)
	}
	if string(body) != "hello world" {
		t.Fatalf("body = %q, want %q", body, "hello world")
	}
}

// startCloseDelimitedHTTPServer answers every request with a body delimited only
// by connection close: no Content-Length, no Transfer-Encoding.
func startCloseDelimitedHTTPServer(t *testing.T, body string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				_, _ = fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n%s", body)
			}(conn)
		}
	}()
	return listener.Addr().String()
}

func startEchoTCPServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buffer := make([]byte, 4)
				if _, err := io.ReadFull(conn, buffer); err != nil {
					return
				}
				_, _ = conn.Write([]byte("pong"))
			}()
		}
	}()
	return listener.Addr().String()
}

func startConnectProxyServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(client net.Conn) {
				defer client.Close()
				reader := bufio.NewReader(client)
				request, err := http.ReadRequest(reader)
				if err != nil || request.Method != http.MethodConnect {
					_, _ = io.WriteString(client, "HTTP/1.1 400 Bad Request\r\n\r\n")
					return
				}
				upstream, err := net.Dial("tcp", request.Host)
				if err != nil {
					_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer upstream.Close()
				_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
				go func() { _, _ = io.Copy(upstream, reader); _ = upstream.Close() }()
				_, _ = io.Copy(client, upstream)
			}(conn)
		}
	}()
	return listener.Addr().String()
}

func newForwardingProxy(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := r.Clone(r.Context())
		out.RequestURI = ""
		response, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			w.Header()[name] = append([]string(nil), values...)
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
}

func startAdapter(t *testing.T, proxyURL string) *Server {
	t.Helper()
	adapter, err := New(Config{ProxyURL: proxyURL, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	return adapter
}

func closeAdapter(t *testing.T, adapter *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := adapter.Close(ctx); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}
