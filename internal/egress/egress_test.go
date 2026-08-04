package egress

import (
	"context"
	"io"
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

func TestRejectsNonLoopbackListener(t *testing.T) {
	adapter, err := New(Config{ProxyURL: "http://proxy.example", Addr: "0.0.0.0:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err == nil {
		t.Fatal("Start succeeded with a public listener")
	}
}
