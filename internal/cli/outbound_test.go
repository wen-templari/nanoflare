package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestOutboundProjectConfigAndResolution(t *testing.T) {
	for _, test := range []struct{ path, content string }{
		{"wrangler.jsonc", `{"name":"host","main":"worker.js","compatibility_date":"2025-12-10","dispatch_namespaces":[{"binding":"D","namespace":"sites","outbound":{"service":"policy","entrypoint":"Policy","parameters":["context"]}}]}`},
		{"wrangler.toml", "name='host'\nmain='worker.js'\ncompatibility_date='2025-12-10'\n[[dispatch_namespaces]]\nbinding='D'\nnamespace='sites'\n[dispatch_namespaces.outbound]\nservice='policy'\nentrypoint='Policy'\nparameters=['context']\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			project, err := parseProjectConfig(test.path, []byte(test.content), false)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/workers":
					_ = json.NewEncoder(w).Encode([]nanoflare.App{{ID: "policy-id", Name: "policy"}})
				case "/v1/dispatch-namespaces":
					_ = json.NewEncoder(w).Encode([]nanoflare.DispatchNamespace{{ID: "ns-id", Name: "sites"}})
				default:
					t.Errorf("unexpected resolution request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			runner := NewRunner(io.Discard, io.Discard)
			if err = runner.resolveProjectBindings(server.URL, &project, false); err != nil {
				t.Fatal(err)
			}
			binding := project.DispatchNamespaces[0]
			if binding.NamespaceID != "ns-id" || binding.Outbound.WorkerID != "policy-id" || binding.Outbound.Entrypoint != "Policy" || binding.Outbound.Parameters[0] != "context" {
				t.Fatalf("resolved binding: %+v", binding)
			}
		})
	}
	for _, outbound := range []string{`null`, `{}`, `{"service":"x","worker_id":"y"}`, `{"service":"x","typo":true}`} {
		_, _, err := decodeProjectConfig("wrangler.json", []byte(`{"dispatch_namespaces":[{"binding":"D","namespace":"sites","outbound":`+outbound+`}]}`))
		if err == nil {
			t.Fatalf("accepted invalid outbound %s", outbound)
		}
	}
}

func TestOutboundProjectEnvironmentResolution(t *testing.T) {
	project, err := parseProjectConfig("wrangler.json", []byte(`{"name":"host","main":"worker.js","compatibility_date":"2025-12-10","dispatch_namespaces":[{"binding":"D","namespace_id":"ns","outbound":{"service":"policy","environment":"staging","entrypoint":"Policy"}}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workers":
			_ = json.NewEncoder(w).Encode([]nanoflare.App{{ID: "production-id", Name: "policy"}})
		case "/v1/workers/production-id/environments":
			_ = json.NewEncoder(w).Encode([]nanoflare.ServiceEnvironment{{Name: "production", WorkerID: "production-id"}, {Name: "staging", WorkerID: "staging-id"}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err = NewRunner(io.Discard, io.Discard).resolveProjectBindings(server.URL, &project, false); err != nil {
		t.Fatal(err)
	}
	if got := project.DispatchNamespaces[0].Outbound; got.WorkerID != "staging-id" || got.Entrypoint != "Policy" {
		t.Fatalf("binding=%+v", got)
	}
}
