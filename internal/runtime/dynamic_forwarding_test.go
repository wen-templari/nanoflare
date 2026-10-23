package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDynamicResourceRedirectIsNotFollowed(t *testing.T) {
	m, _, _, key := newDynamicControlFixture(t)
	var followed atomic.Bool
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true); w.WriteHeader(200) }))
	defer trap.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, trap.URL, 302) }))
	defer backend.Close()
	m.options.RuntimeAddr = backend.URL
	w := dynamicBrokerRequest(t, m, "resource", key, "", "KV", nil)
	if w.Code != 302 || followed.Load() {
		t.Fatalf("resource bridge followed a redirect: status=%d followed=%t", w.Code, followed.Load())
	}
}

func TestDynamicResourceStreamFlushesAndCancelsUpstream(t *testing.T) {
	m, _, _, key := newDynamicControlFixture(t)
	canceled := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer backend.Close()
	m.options.RuntimeAddr = backend.URL
	broker := httptest.NewServer(http.HandlerFunc(m.serveBroker))
	defer broker.Close()
	client := &http.Client{Timeout: time.Second}
	r, _ := http.NewRequest("GET", broker.URL+"/resource/"+key+"/stream", nil)
	r.Header.Set("X-Nanoflare-Broker-Token", m.brokerToken)
	r.Header.Set("X-Nanoflare-Binding-Name", "KV")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, err = io.ReadFull(response.Body, b)
	response.Body.Close()
	if err != nil || string(b) != "first\n" {
		t.Fatalf("stream: %q %v", b, err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("resource bridge retained upstream after client disconnected")
	}
}

func TestDynamicResourceIdentityOnlyReachesAccessVerifier(t *testing.T) {
	m, _, active, key := newDynamicControlFixture(t)
	endpoint, err := m.Ensure(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Release()
	token := endpoint.Headers.Get("X-Nanoflare-Invocation")
	for _, binding := range []string{"__NANOFLARE_ACCESS", "KV", "__NANOFLARE_LOG_COLLECTOR"} {
		t.Run(binding, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantAssertion := ""
				wantToken := key
				if binding != "KV" {
					wantToken = token
				}
				if binding == "__NANOFLARE_ACCESS" {
					wantAssertion = "signed-assertion"
					if r.URL.Path != "/internal/runtime/access" {
						t.Errorf("access path = %s", r.URL.Path)
					}
				}
				if r.Header.Get("X-Nanoflare-User-JWT") != wantAssertion {
					t.Error("incorrect assertion forwarding")
				}
				if r.Header.Get("Authorization") != "Bearer "+wantToken {
					t.Error("resource authority not reconstructed")
				}
				for _, name := range []string{"X-Nanoflare-User-Email", "X-Nanoflare-Runner-Token", "X-Nanoflare-Broker-Token", "X-Nanoflare-Binding-Name", "X-Nanoflare-Invocation"} {
					if r.Header.Get(name) != "" {
						t.Errorf("unexpected forwarded header %s", name)
					}
				}
				w.WriteHeader(204)
			}))
			defer backend.Close()
			m.options.RuntimeAddr = backend.URL
			r := httptest.NewRequest("GET", "/resource/"+key+"/identity", nil)
			r.Header.Set("X-Nanoflare-Broker-Token", m.brokerToken)
			r.Header.Set("X-Nanoflare-Invocation", token)
			r.Header.Set("X-Nanoflare-Binding-Name", binding)
			r.Header.Set("X-Nanoflare-User-JWT", "signed-assertion")
			r.Header.Set("X-Nanoflare-User-Email", "user@example.com")
			r.Header.Set("X-Nanoflare-Runner-Token", "forged")
			r.Header.Set("Authorization", "Bearer forged")
			w := httptest.NewRecorder()
			m.serveBroker(w, r)
			if w.Code != 204 {
				t.Fatalf("resource status = %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
