package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestReuseProjectBindings(t *testing.T) {
	for _, tc := range []struct {
		name        string
		second      string
		missing     bool
		readFailure bool
		wantError   string
	}{
		{name: "reuse renamed resources"},
		{name: "agreeing split traffic", second: "kv-id"},
		{name: "conflicting split traffic", second: "different", wantError: "disagree"},
		{name: "deleted resource", missing: true, wantError: "reuse binding"},
		{name: "read failure", readFailure: true, wantError: "read active resource bindings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := Project{Name: "renamed-worker", KVNamespaces: []nanoflare.KVBinding{{Binding: "CACHE"}}, Databases: []nanoflare.DatabaseBinding{{Binding: "DB"}}, ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "FILES"}}}
			active := []nanoflare.WorkerDeployment{{ID: "current", TrafficPercent: 100, KVNamespaces: []nanoflare.KVBinding{{Binding: "CACHE", ID: "kv-id"}}, Databases: []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: "db-id"}}, ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "FILES", BucketID: "r2-id"}}},
				{ID: "inactive", TrafficPercent: 0, KVNamespaces: []nanoflare.KVBinding{{Binding: "CACHE", ID: "ignored"}}}}
			if tc.second != "" {
				active[0].TrafficPercent = 50
				active = append(active, nanoflare.WorkerDeployment{ID: "other", TrafficPercent: 50, KVNamespaces: []nanoflare.KVBinding{{Binding: "CACHE", ID: tc.second}}})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("unexpected mutation: %s", r.Method)
				}
				switch r.URL.Path {
				case "/v1/workers/worker/active-bindings":
					if tc.readFailure {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					writeJSON(t, w, 200, active)
				case "/v1/db":
					if tc.missing {
						writeJSON(t, w, 200, []nanoflare.Database{})
						return
					}
					writeJSON(t, w, 200, []nanoflare.Database{{ID: "db-id", Name: "renamed"}})
				case "/v1/kv/namespaces/kv-id", "/v1/object-storage-buckets/r2-id":
					if tc.missing {
						http.NotFound(w, r)
						return
					}
					writeJSON(t, w, 200, map[string]string{"id": "existing"})
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			err := NewRunner(io.Discard, io.Discard).reuseProjectBindings(server.URL, "worker", &project)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				if project.KVNamespaces[0].ID != "" || project.Databases[0].DatabaseID != "" || project.ObjectStorageBuckets[0].BucketID != "" {
					t.Fatal("failed resolution mutated IDs")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if project.KVNamespaces[0].ID != "kv-id" || project.Databases[0].DatabaseID != "db-id" || project.ObjectStorageBuckets[0].BucketID != "r2-id" {
				t.Fatalf("resolved: %#v", project)
			}
			if project.expectedActiveTraffic == nil || len(*project.expectedActiveTraffic) != 1+btoi(tc.second != "") {
				t.Fatal("missing traffic precondition")
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestExplicitResourcesSkipBindingReuse(t *testing.T) {
	project := Project{KVNamespaces: []nanoflare.KVBinding{{Binding: "CACHE", ID: "explicit"}}, Databases: []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: "explicit"}}, ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "FILES"}}, objectStorageBucketNames: map[string]string{"FILES": "uploads"}}
	if err := NewRunner(io.Discard, io.Discard).reuseProjectBindings("invalid", "worker", &project); err != nil {
		t.Fatal(err)
	}
}
