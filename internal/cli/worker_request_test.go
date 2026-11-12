package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWorkersRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		extra     []string
		wantError bool
	}{
		{name: "success", status: 200},
		{name: "failure", status: 403, wantError: true},
		{name: "inspect-failure", status: 403, extra: []string{"--fail=false"}},
		{name: "redirect", status: 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var redirectHits atomic.Int32
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectHits.Add(1) }))
			defer redirect.Close()
			worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.RequestURI() != "/api/a%2Fb?q=1" {
					t.Errorf("target=%s %s", r.Method, r.URL.RequestURI())
				}
				if r.Header.Get(nanoflare.WorkerAccessHeader) != "temporary-worker-token" || r.Header.Get("Authorization") != "Bearer app-token" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("headers=%v", r.Header)
				}
				data, _ := io.ReadAll(r.Body)
				if string(data) != `{"ok":true}` {
					t.Errorf("body=%q", data)
				}
				w.Header().Set("Location", redirect.URL)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "worker body")
			}))
			defer worker.Close()
			host := strings.TrimPrefix(worker.URL, "https://")
			var exchanges int
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer control-token" {
					t.Error("control credential missing")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /v1/organizations/org/workers":
					json.NewEncoder(w).Encode([]nanoflare.App{{ID: "worker-id", Name: "my-worker", Hostname: host}})
				case "POST /v1/organizations/org/workers/worker-id/access-token":
					exchanges++
					var input nanoflare.WorkerAccessRequest
					json.NewDecoder(r.Body).Decode(&input)
					if input.Method != "POST" || input.Path != "/api/a%2Fb?q=1" {
						t.Errorf("exchange=%+v", input)
					}
					json.NewEncoder(w).Encode(nanoflare.WorkerAccessToken{Token: "temporary-worker-token", URL: "https://untrusted.invalid/", ExpiresAt: time.Now().Add(time.Minute)})
				default:
					t.Errorf("unexpected API request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			t.Setenv(authTokenEnv, "control-token")
			t.Setenv(authOrgIDEnv, "org")
			var stdout, stderr bytes.Buffer
			runner := NewRunner(&stdout, &stderr)
			runner.Client = worker.Client()
			runner.Stdin = strings.NewReader(`{"ok":true}`)
			args := []string{"workers", "request", "my-worker", "/api/a%2Fb?q=1", "--api-url", api.URL, "-X", "POST", "-H", "Authorization: Bearer app-token", "-H", "Content-Type: application/json", "-d", "-", "-i"}
			err := runner.Run(append(args, tc.extra...))
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if exchanges != 1 || !strings.Contains(stdout.String(), "worker body") || !strings.Contains(stdout.String(), fmt.Sprintf("HTTP/1.1 %d", tc.status)) {
				t.Fatalf("exchanges=%d output=%s", exchanges, stdout.String())
			}
			if redirectHits.Load() != 0 {
				t.Fatal("followed redirect and risked leaking credentials")
			}
			if strings.Contains(stdout.String()+stderr.String(), "temporary-worker-token") {
				t.Fatal("worker credential printed")
			}
		})
	}
}

func TestWorkersRequestRejectsUnsafeInputBeforeNetwork(t *testing.T) {
	for _, extra := range [][]string{{"https://evil.example/path"}, {"//evil.example/path"}, {"/", "-H", "X-Nanoflare-Access-Token: forged"}, {"/", "-H", "Host: evil.example"}, {"/", "-H", "X-Forwarded-Host: evil.example"}, {"/", "--timeout", "0s"}, {"/", "-X", "CONNECT"}} {
		runner := NewRunner(io.Discard, io.Discard)
		err := runner.Run(append([]string{"workers", "request", "worker"}, extra...))
		if err == nil {
			t.Fatalf("accepted unsafe arguments %q", extra)
		}
	}
}
