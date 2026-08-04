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

func TestAdapterForwardsAbsoluteHTTPRequestThroughConfiguredProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Origin", "yes")
		_, _ = w.Write([]byte("proxied"))
	}))
	defer target.Close()

	requests := make(chan *http.Request, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		out := r.Clone(r.Context())
		out.RequestURI = ""
		response, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.Header().Set("X-Origin", response.Header.Get("X-Origin"))
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()

	adapter, err := New(Config{ProxyURL: proxy.URL, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = adapter.Close(ctx)
	}()

	adapterURL, _ := url.Parse("http://" + adapter.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(adapterURL)}}
	response, err := client.Get(target.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Header.Get("X-Origin") != "yes" {
		t.Fatalf("response did not come from origin: %#v", response.Header)
	}
	select {
	case got := <-requests:
		if got.URL.String() != target.URL+"/stream" {
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

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := r.Clone(r.Context())
		out.RequestURI = ""
		response, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()

	adapter, err := New(Config{ProxyURL: proxy.URL, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = adapter.Close(ctx)
	}()

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
	for i, path := range []string{"/first", "/second"} {
		connection := ""
		if i == 1 {
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
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err == nil {
		response.Body.Close()
		t.Fatalf("idle connection received unexpected response: %s", response.Status)
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("idle connection read error = %v, want a silent close", err)
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
		t.Fatalf("partial request response = %s, want 400", response.Status)
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

func TestRejectsNonLoopbackListener(t *testing.T) {
	adapter, err := New(Config{ProxyURL: "http://proxy.example", Addr: "0.0.0.0:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err == nil {
		t.Fatal("Start succeeded with a public listener")
	}
}
