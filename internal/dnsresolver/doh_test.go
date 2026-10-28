package dnsresolver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoHAnswersAndFailures(t *testing.T) {
	server, err := New(Config{DefaultProfile: "private", Profiles: map[string]Profile{
		"private": {Servers: []string{startTestDNSServer(t)}, Timeout: "1s"},
		"slow":    {Servers: []string{startBlackholeDNSServer(t)}, Timeout: "10ms"},
	}}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query, profile            string
		status, count, recordType int
	}{
		{"name=rac.internal&type=A", "", 0, 2, 1},
		{"name=rac.internal&type=1", "", 0, 2, 1},
		{"name=rac.internal&type=AAAA", "", 3, 0, 0},
		{"name=fd00::1&type=28", "", 0, 1, 28},
		{"name=fd00::1&type=A", "", 3, 0, 0},
		{"name=rac.internal&type=TXT", "", 3, 0, 0},
		{"name=rac.internal&type=MX", "", 3, 0, 0},
		{"name=rac.internal&type=CAA", "", 4, 0, 0},
		{"name=::ffff:10.0.0.1&type=AAAA", "", 0, 1, 28},
		{"name=::ffff:10.0.0.1&type=A", "", 3, 0, 0},
		{"name=rac.internal&type=A", "unknown", 2, 0, 0},
		{"name=rac.internal&type=A", "slow", 2, 0, 0},
		{"type=A", "", 2, 0, 0},
	} {
		t.Run(tc.query+tc.profile, func(t *testing.T) {
			r := httptest.NewRequest("GET", DoHPath+"?"+tc.query, nil)
			r.Header.Set("X-Nanoflare-DNS-Profile", tc.profile)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			var response dohResponse
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Status != tc.status || len(response.Answer) != tc.count {
				t.Fatalf("response %d %s", w.Code, w.Body)
			}
			for _, answer := range response.Answer {
				if answer.Type != tc.recordType || answer.TTL != 30 || answer.Name != r.URL.Query().Get("name") {
					t.Fatalf("answer: %+v", answer)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", DoHPath+"?name=rac.internal&type=A", nil).WithContext(ctx))
	if w.Body.Len() != 0 {
		t.Fatalf("canceled resolution wrote %s", w.Body)
	}
}

type dohTestTransport func(*http.Request) (*http.Response, error)

func (f dohTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoHTransportIsolationAndMatching(t *testing.T) {
	fallbackErr := errors.New("ordinary egress")
	next := dohTestTransport(func(r *http.Request) (*http.Response, error) { return nil, fallbackErr })
	calls := 0
	transport := DoHTransport(next, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Host != "nanoflare.dns" || r.URL.Path != DoHPath || r.URL.Query().Get("name") != "private.internal" || r.URL.Query().Get("profile") != "" || len(r.Header) != 1 || r.Header.Get("Accept") != "application/dns-json" {
			t.Errorf("untrusted request data reached resolver: %s %v", r.URL, r.Header)
		}
		writeJSON(w, 200, dohResponse{Status: 3})
	}))
	for _, tc := range []struct {
		method, url string
		intercepted bool
	}{
		{"GET", "https://cloudflare-dns.com/dns-query?name=private.internal&type=A&profile=other", true},
		{"GET", "https://CLOUDFLARE-DNS.COM/dns-query?name=private.internal&type=TXT", true},
		{"GET", "https://cloudflare-dns.com./dns-query?name=private.internal&type=A", true},
		{"GET", "https://cloudflare-dns.com.:443/dns-query?name=private.internal&type=A", true},
		{"GET", "https://cloudflare-dns.com/dns-query?dns=AAAB", false},
		{"GET", "https://cloudflare-dns.com/dns-query", false},
		{"POST", "https://cloudflare-dns.com/dns-query", false},
		{"GET", "http://cloudflare-dns.com/dns-query", false},
		{"GET", "https://cloudflare-dns.com/other", false},
		{"GET", "https://cloudflare-dns.com.attacker.test/dns-query", false},
		{"GET", "https://user@cloudflare-dns.com/dns-query", false},
	} {
		r, _ := http.NewRequest(tc.method, tc.url, nil)
		r.Header.Set("Authorization", "secret")
		r.Header.Set("X-Nanoflare-DNS-Profile", "other")
		response, err := transport.RoundTrip(r)
		if !tc.intercepted {
			if !errors.Is(err, fallbackErr) {
				t.Fatalf("ordinary egress changed: %s %v", tc.url, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if !strings.Contains(string(body), `"Status":3`) {
			t.Fatalf("response %s", body)
		}
		if r.Header.Get("Authorization") != "secret" {
			t.Fatal("mutated caller request")
		}
	}
	if calls != 4 {
		t.Fatalf("resolver calls %d", calls)
	}
	r, _ := http.NewRequest("GET", "https://cloudflare-dns.com/dns-query?name=private.internal&type=A", nil)
	response, err := DoHTransport(next, nil).RoundTrip(r)
	if err != nil || response.StatusCode != 503 {
		t.Fatalf("missing resolver: %v %v", response, err)
	}
	response.Body.Close()
}

func TestDoHTransportCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", "https://cloudflare-dns.com/dns-query?name=private.internal&type=A", nil)
	transport := DoHTransport(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		if r.Context().Err() == nil {
			t.Fatal("context was not propagated")
		}
		writeJSON(w, 200, dohResponse{Status: 2})
	}))
	if response, err := transport.RoundTrip(r); response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v %v", response, err)
	}
}
