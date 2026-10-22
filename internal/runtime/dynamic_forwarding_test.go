package runtime

import (
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
