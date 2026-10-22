package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestElapsedMetricNeverClaimsCPUAccounting(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), discardWriter{})
	worker, err := service.CreateApp(nanoflare.CreateAppInput{Name: "elapsed", Hostname: "elapsed.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	telemetry := runtime.NewDurationTelemetry()
	telemetry.RecordBatch([]runtime.DurationTraceEvent{{ScriptName: worker.ID, DurationMs: 20}})
	server := newTestServer(service)
	server.SetDurationTelemetry(telemetry)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, "nanoflare_worker_duration_milliseconds_p90") || strings.Contains(body, "nanoflare_worker_cpu_time") {
		t.Fatalf("misleading metric semantics: %d %s", response.Code, body)
	}
}

func TestGlobalMetricsNeedsOperatorCredential(t *testing.T) {
	repo := nanoflare.NewStore()
	service := nanoflare.NewService(repo, discardWriter{})
	auth := nanoflare.NewControlAuthService(repo, "operator-test")
	server := NewServerWithRuntime(service, nil, "", nil, auth, nil)
	request := func(token string) int {
		r := httptest.NewRequest("GET", "/metrics", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code
	}
	if status := request(""); status != 404 {
		t.Fatalf("exporter was not disabled without an operator secret: %d", status)
	}
	server.SetMetricsToken("operator-only")
	for _, token := range []string{"", "organization-token", "nfc1.worker", "nfi1.invocation"} {
		if status := request(token); status != 401 {
			t.Fatalf("non-operator metrics token accepted: %d", status)
		}
	}
	if status := request("operator-only"); status != 200 {
		t.Fatalf("operator metrics denied: %d", status)
	}
}
