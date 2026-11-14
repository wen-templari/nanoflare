package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// Exercise production admission/effect authority without native runtime timing or
// test-only changes to budget counters. The trusted effect API batches unit costs.
func TestOutboundSharedBudgetAndDepth(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			service, _ := managedService(t, repo, discardWriter{})
			ctx := context.Background()
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "budget-customers"})
			must(err)
			customerKV, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "org", Name: "customer-state"})
			must(err)
			policyKV, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "org", Name: "policy-state"})
			must(err)
			_, err = service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: nanoflare.RuntimeKV, ResourceID: customerKV.ID}}})
			must(err)
			input := nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {fetch(){return new Response('unused')}}"}}}
			customer, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "customer", Execution: "dynamic"})
			must(err)
			input.KVNamespaces = []nanoflare.KVBinding{{Binding: "STATE", ID: customerKV.ID}}
			_, err = service.Deploy(customer.ID, input)
			must(err)
			policy, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "policy", Hostname: "policy.test"})
			must(err)
			input.KVNamespaces = []nanoflare.KVBinding{{Binding: "STATE", ID: policyKV.ID}}
			_, err = service.Deploy(policy.ID, input)
			must(err)
			input.Services = []nanoflare.ServiceBinding{{Binding: "RECURSE", WorkerID: policy.ID}}
			_, err = service.Deploy(policy.ID, input)
			must(err)
			host, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "host", Hostname: "host.test"})
			must(err)
			input.Services = nil
			input.KVNamespaces = nil
			input.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: policy.ID}}}
			revision, err := service.Deploy(host.ID, input)
			must(err)
			active := nanoflare.ActiveDeployment{App: host, Deployment: revision}
			transport := nanoflare.RuntimeCapability(active, nanoflare.RuntimeDispatch, ns.ID)
			admit := func() (string, nanoflare.DispatchArtifact, nanoflare.DispatchArtifact) {
				t.Helper()
				root, _, err := service.BeginDispatchRoot(active)
				must(err)
				t.Cleanup(func() { service.EndDispatchRoot(root, true) })
				customer, err := service.AcquireDispatchBinding(ctx, transport, ns.ID, "customer", root, "CUSTOMERS", nil)
				must(err)
				policy, err := service.AcquireOutboundDispatch(ctx, transport, customer.Lease)
				must(err)
				return root, customer, policy
			}
			_, parent, target := admit()
			for _, check := range []struct{ lease, resource string }{{parent.Lease, policyKV.ID}, {target.Lease, customerKV.ID}} {
				if _, err = service.AuthorizeRuntime(ctx, check.lease, nanoflare.RuntimeKV, check.resource); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
					t.Fatalf("cross-worker authority widened: %v", err)
				}
			}
			_, err = service.DispatchEffect(ctx, transport, parent.Lease, nanoflare.RuntimeKV, customerKV.ID, 1)
			must(err)
			_, err = service.DispatchEffect(ctx, transport, target.Lease, nanoflare.RuntimeOutbound, "", 1)
			must(err)
			// 10,000 shared units - customer admission - policy admission - customer KV
			// effect - target forward = 9,996. Exact exhaustion proves policy admission
			// is not double charged and customer/target effects debit the same root.
			_, err = service.DispatchEffect(ctx, transport, target.Lease, nanoflare.RuntimeOutbound, "", 9996)
			must(err)
			_, err = service.DispatchEffect(ctx, transport, target.Lease, nanoflare.RuntimeOutbound, "", 1)
			if !errors.Is(err, nanoflare.ErrSubrequestLimit) {
				t.Fatalf("target escaped exhausted root: %v", err)
			}
			_, err = service.DispatchEffect(ctx, transport, parent.Lease, nanoflare.RuntimeKV, customerKV.ID, 1)
			if !errors.Is(err, nanoflare.ErrSubrequestLimit) {
				t.Fatalf("customer escaped exhausted root: %v", err)
			}
			handler := NewRuntimeDispatchServer(service, nil)
			request := httptest.NewRequest(http.MethodGet, "/internal/runtime/dispatch/outbound", nil)
			request.Header.Set("Authorization", "Bearer "+transport)
			request.Header.Set("X-Nanoflare-Invocation", target.Lease)
			request.Header.Set("X-Nanoflare-Outbound-URL", "https://must-not-be-contacted.invalid/")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 429 {
				t.Fatalf("exhaustion HTTP status: %d %s", response.Code, response.Body.String())
			}
			root, _, recursive := admit()
			// Customer is depth 0, initial outbound target depth 1; service calls through
			// that target's declared self-binding preserve ordinary target authority.
			for depth := 2; depth <= 8; depth++ {
				recursive, err = service.AcquireServiceDispatch(ctx, transport, recursive.Lease, "RECURSE")
				must(err)
				if recursive.Deployment.AppID != policy.ID || !recursive.OutboundExecution {
					t.Fatalf("depth %d changed target authority", depth)
				}
			}
			_, err = service.AcquireServiceDispatch(ctx, transport, recursive.Lease, "RECURSE")
			if !errors.Is(err, nanoflare.ErrDispatchDepth) {
				t.Fatalf("recursive depth limit not enforced: %v", err)
			}
			payload := fmt.Sprintf(`{"lease":%q,"binding":"RECURSE"}`, recursive.Lease)
			request = httptest.NewRequest(http.MethodPost, "/internal/runtime/dispatch/nested", strings.NewReader(payload))
			request.Header.Set("Authorization", "Bearer "+transport)
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 429 {
				t.Fatalf("depth HTTP status: %d %s", response.Code, response.Body.String())
			}
			effect, cancel, err := service.DispatchEffectContext(ctx, recursive.Lease)
			must(err)
			defer cancel()
			service.EndDispatchRoot(root, true)
			select {
			case <-effect.Done():
			case <-time.After(time.Second):
				t.Fatal("root cancellation did not cancel retained target effect")
			}
			_, err = service.DispatchEffect(ctx, transport, recursive.Lease, nanoflare.RuntimeOutbound, "", 1)
			if err == nil {
				t.Fatal("cancelled recursive target retained outbound authority")
			}
			_, err = service.AcquireServiceDispatch(ctx, transport, recursive.Lease, "RECURSE")
			if err == nil {
				t.Fatal("cancelled target retained nested admission authority")
			}
		})
	}
}
