package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestPoolMetricsKeepUnknownAndStaleUsageUnavailable(t *testing.T) {
	server := NewServer(nanoflare.NewService(nanoflare.NewStore(), discardWriter{}))
	server.SetMetricsToken("operator-only")
	var snapshot *nanoflare.RunnerResources
	observed := time.Time{}
	server.SetRunnerResourceReader(func() (*nanoflare.RunnerResources, time.Time) { return snapshot, observed })
	scrape := func(token string) (int, string) {
		request := httptest.NewRequest("GET", "/metrics", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response.Code, response.Body.String()
	}
	if code, _ := scrape("customer"); code != 401 {
		t.Fatal("customer read physical pool metrics")
	}
	_, body := scrape("operator-only")
	if !strings.Contains(body, "nanoflare_runner_resources_available 0") || strings.Contains(body, "nanoflare_runner_memory_current_bytes ") {
		t.Fatal("unknown usage presented as measured zero")
	}
	snapshot = &nanoflare.RunnerResources{MemoryBytes: 64, MemoryLimitBytes: 128, MemoryLimited: true, MemoryPeakBytes: 80, MemoryPeakAvailable: true, PIDs: 12, PIDLimit: 128, PIDLimited: true, OOMKills: 2}
	observed = time.Now()
	_, body = scrape("operator-only")
	for _, want := range []string{"nanoflare_runner_resources_available 1", "nanoflare_runner_memory_current_bytes 64", "nanoflare_runner_memory_limit_bytes 128", "nanoflare_runner_memory_peak_bytes 80", "nanoflare_runner_pids_current 12", "nanoflare_runner_oom_kills_total 2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing operator measurement %s", want)
		}
	}
	if strings.Contains(body, "nanoflare_worker_cpu") {
		t.Fatal("physical memory metrics fabricated CPU accounting")
	}
	observed = time.Now().Add(-time.Minute)
	_, body = scrape("operator-only")
	if !strings.Contains(body, "nanoflare_runner_resources_available 0") || strings.Contains(body, "nanoflare_runner_memory_current_bytes ") {
		t.Fatal("stale usage presented as current")
	}
	snapshot.MemoryLimited = false
	snapshot.PIDLimited = false
	snapshot.MemoryPeakAvailable = false
	observed = time.Now()
	_, body = scrape("operator-only")
	if !strings.Contains(body, "nanoflare_runner_resources_available 1") || strings.Contains(body, "nanoflare_runner_memory_limit_bytes ") || strings.Contains(body, "nanoflare_runner_pids_limit ") || strings.Contains(body, "nanoflare_runner_memory_peak_bytes ") {
		t.Fatal("unlimited/unknown bounds presented as finite")
	}
}
