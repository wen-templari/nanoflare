package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDispatchOutboundPolicyDenialDiagnostics(t *testing.T) {
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := nanoflare.NewStore()
	must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
	service := nanoflare.NewService(repo, discardWriter{})
	service.SetPlatformDispatchEnabled(true)
	ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "production"})
	must(err)
	customer, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
	must(err)
	input := nanoflare.DeployInput{Format: "modules", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, CompatibilityDate: "2025-12-10"}
	_, err = service.Deploy(customer.ID, input)
	must(err)
	platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "platform", Hostname: "dispatch.example.com"})
	must(err)
	input.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: ns.ID}}
	caller, err := service.Deploy(platform.ID, input)
	must(err)
	token := nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: platform, Deployment: caller}, nanoflare.RuntimeDispatch, ns.ID)
	artifact, err := service.AcquireDispatch(context.Background(), token, ns.ID, "alpha")
	must(err)
	defer service.FinishDispatch(context.Background(), token, artifact.Lease, true)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("denied WFP outbound reached proxy") }))
	defer proxy.Close()
	transport, err := egress.NewHTTPTransport(egress.Config{NetworkAllow: []string{"public"}, ProxyURL: proxy.URL, ProxyResolves: true})
	must(err)
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	request := httptest.NewRequest("GET", "http://runtime/internal/runtime/dispatch/outbound", strings.NewReader("secret-body"))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Nanoflare-Invocation", artifact.Lease)
	request.Header.Set("X-Nanoflare-Outbound-URL", "http://169.254.169.254/private-path?secret-query")
	request.Header.Set("X-Nanoflare-Outbound-Authorization", "secret-authorization")
	response := httptest.NewRecorder()
	NewRuntimeDispatchServer(service, nil, transport).ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatalf("status %d: %s", response.Code, response.Body)
	}
	for _, want := range []string{"169.254.169.254", "\"port\":\"80\"", "http_proxy", "address_not_allowed"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing diagnostic %s: %s", want, &logs)
		}
	}
	for _, secret := range []string{token, artifact.Lease, "private-path", "secret-query", "secret-body", "secret-authorization"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log leaked request data")
		}
	}
}
