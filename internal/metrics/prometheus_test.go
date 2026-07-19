package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrafficQueriesRouterMetrics(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		queries = append(queries, query)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "query_range"):
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"values":[[1,"2.5"],[2,"3.5"]]}]}}`)
		case strings.Contains(query, "duration"):
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"value":[1,"0.084"]}]}}`)
		case strings.Contains(query, `code=~"5.."`):
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"value":[1,"0.25"]}]}}`)
		case strings.Contains(query, "sum by (code)"):
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{"code":"200"},"value":[1,"3.75"]},{"metric":{"code":"500"},"value":[1,"0.25"]}]}}`)
		default:
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"value":[1,"4"]}]}}`)
		}
	}))
	defer server.Close()

	traffic, err := NewClient(server.URL).Traffic("integration")
	if err != nil {
		t.Fatal(err)
	}
	if !traffic.Available || traffic.RequestsPerSecond != 4 || traffic.P95Latency != 0.084 || traffic.ErrorRate != 0.0625 {
		t.Fatalf("unexpected traffic: %#v", traffic)
	}
	if len(traffic.Traffic) != 2 || traffic.Traffic[1] != 3.5 || len(traffic.StatusCodes) != 2 {
		t.Fatalf("unexpected traffic series: %#v", traffic)
	}
	if traffic.Invocations != 4 || traffic.Errors != 0.25 {
		t.Fatalf("unexpected traffic totals: %#v", traffic)
	}
	if len(queries) != 6 {
		t.Fatalf("got %d queries, want 6", len(queries))
	}
	for _, query := range queries {
		if !strings.Contains(query, `router=~"integration@(http|file)"`) {
			t.Fatalf("query is not router-provider scoped: %s", query)
		}
	}
	if !queryWasSent(queries, `sum(increase(traefik_router_requests_total{router=~"integration@(http|file)"}[24h]))`) {
		t.Fatalf("missing 24h invocation query: %#v", queries)
	}
	if !queryWasSent(queries, `sum(increase(traefik_router_requests_total{router=~"integration@(http|file)",code=~"5.."}[24h]))`) {
		t.Fatalf("missing 24h error query: %#v", queries)
	}
	if !queryWasSent(queries, `sum(increase(traefik_router_requests_total{router=~"integration@(http|file)"}[5m]))`) {
		t.Fatalf("missing bucketed traffic query: %#v", queries)
	}
}

func TestDatabaseMetricsTimeseriesQueriesPrometheus(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"result":[{"values":[[1,"2"],[2,"3"]]}]}}`)
	}))
	defer server.Close()

	series, err := NewClient(server.URL).DatabaseMetricsTimeseries("db_123")
	if err != nil {
		t.Fatal(err)
	}
	if !series.Available || len(series.Queries) != 2 || series.Queries[1].Value != 3 || series.Queries[0].Timestamp.IsZero() {
		t.Fatalf("unexpected database series: %#v", series)
	}
	if len(queries) != 10 {
		t.Fatalf("got %d queries, want 10: %#v", len(queries), queries)
	}
	for _, query := range queries {
		if !strings.Contains(query, `database_id="db_123"`) {
			t.Fatalf("query is not database scoped: %s", query)
		}
	}
	if !queryWasSent(queries, `sum(increase(nanoflare_db_queries_total{database_id="db_123"}[5m]))`) {
		t.Fatalf("missing query count range query: %#v", queries)
	}
	if !queryWasSent(queries, `histogram_quantile(0.99, sum by (le) (rate(nanoflare_db_query_duration_seconds_bucket{database_id="db_123"}[5m]))) * 1000`) {
		t.Fatalf("missing p99 latency range query: %#v", queries)
	}
}

func queryWasSent(queries []string, expected string) bool {
	for _, query := range queries {
		if query == expected {
			return true
		}
	}
	return false
}
