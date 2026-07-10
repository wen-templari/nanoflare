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
	if len(queries) != 7 {
		t.Fatalf("got %d queries, want 7", len(queries))
	}
	for _, query := range queries {
		if !strings.Contains(query, `router=~"integration@(http|file)"`) {
			t.Fatalf("query is not router-provider scoped: %s", query)
		}
	}
}
