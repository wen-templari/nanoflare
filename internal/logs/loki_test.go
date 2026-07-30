package logs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestLokiReaderQuery(t *testing.T) {
	since := time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC)
	until := since.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("path = %q", r.URL.Path)
		}
		values := r.URL.Query()
		if got, want := values.Get("query"), `{worker_id="worker-1",deployment_id="deploy-1",level="error"} |= "needle"`; got != want {
			t.Errorf("query = %q, want %q", got, want)
		}
		if values.Get("start") != "1785286923000000000" || values.Get("end") != "1785290523000000000" {
			t.Errorf("time range = %q to %q", values.Get("start"), values.Get("end"))
		}
		if values.Get("direction") != "backward" || values.Get("limit") != "25" {
			t.Errorf("pagination = direction %q, limit %q", values.Get("direction"), values.Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{"worker_id":"worker-1","deployment_id":"deploy-1","level":"error"},"values":[["1785286925000000000","later"],["1785286924000000000","earlier"]]}]}}`))
	}))
	defer server.Close()

	reader := NewLokiReader(server.URL)
	lines, err := reader.Query(context.Background(), "worker-1", nanoflare.WorkerOutputQuery{
		DeploymentID: "deploy-1", Level: "error", Text: "needle", Since: since, Until: until, Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Message != "earlier" || lines[1].Message != "later" {
		t.Fatalf("lines = %#v", lines)
	}
	if lines[0].AppID != "worker-1" || lines[0].DeploymentID != "deploy-1" || lines[0].Level != "error" {
		t.Errorf("line labels = %#v", lines[0])
	}
}

func TestLokiReaderQueryReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := NewLokiReader(server.URL).Query(context.Background(), "worker-1", nanoflare.WorkerOutputQuery{})
	if err == nil {
		t.Fatal("expected an HTTP error")
	}
}
