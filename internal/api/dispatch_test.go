package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDispatchLeaseAuthorization(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			service := nanoflare.NewService(repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "production"})
			must(err)
			_, err = service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "disabled", Execution: "dynamic"})
			if !errors.Is(err, nanoflare.ErrDispatchUnavailable) {
				t.Fatalf("disabled dynamic profile: %v", err)
			}
			service.SetPlatformDispatchEnabled(true)
			customer, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			a, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "org", Name: "a"})
			must(err)
			b, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "org", Name: "b"})
			must(err)
			ns, err = service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "kv", ResourceID: a.ID}, {Kind: "kv", ResourceID: b.ID}}})
			must(err)
			input := nanoflare.DeployInput{Format: "modules", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, CompatibilityDate: "2025-12-10", KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: a.ID}}}
			first, err := service.Deploy(customer.ID, input)
			must(err)
			platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "platform", Hostname: "dispatch.example.com"})
			must(err)
			platformInput := nanoflare.DeployInput{Format: "modules", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, CompatibilityDate: "2025-12-10", DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: ns.ID}}}
			caller, err := service.Deploy(platform.ID, platformInput)
			must(err)
			callerToken := nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: platform, Deployment: caller}, nanoflare.RuntimeDispatch, ns.ID)
			ctx := context.Background()
			if _, err = service.AcquireDispatch(ctx, platform.RuntimeToken, ns.ID, "alpha"); !errors.Is(err, nanoflare.ErrInvalidCapability) {
				t.Fatalf("management root acquired invocation: %v", err)
			}
			acquired, err := service.AcquireDispatch(ctx, callerToken, ns.ID, "alpha")
			must(err)
			if acquired.Deployment.ID != first.ID {
				t.Fatal("wrong initial revision")
			}
			input.KVNamespaces[0].ID = b.ID
			second, err := service.Deploy(customer.ID, input)
			must(err)
			newer, err := service.AcquireDispatch(ctx, callerToken, ns.ID, "alpha")
			must(err)
			if newer.Deployment.ID != second.ID {
				t.Fatal("new invocation retained old selection")
			}
			old, err := service.AuthorizeRuntime(ctx, acquired.Lease, "kv", a.ID)
			must(err)
			if old.Deployment.ID != first.ID {
				t.Fatal("old invocation switched revisions")
			}
			if _, err = service.AuthorizeRuntime(ctx, acquired.Lease, "kv", b.ID); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("old invocation acquired new binding: %v", err)
			}
			if _, err = service.AuthorizeRuntime(ctx, newer.Lease, "kv", a.ID); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("new invocation acquired old binding: %v", err)
			}
			if _, err = service.DispatchEffect(ctx, "wrong-caller", acquired.Lease, "kv", a.ID, 1); !errors.Is(err, nanoflare.ErrInvocationExpired) {
				t.Fatalf("substituted caller: %v", err)
			}
			if _, err = service.DispatchResource(ctx, acquired.Lease, "kv", "OTHER"); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("undeclared binding: %v", err)
			}
			// Reservations are atomic across parallel resource effects.
			var allowed, denied atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 120; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, e := service.DispatchEffect(ctx, callerToken, acquired.Lease, "kv", a.ID, 1)
					if e == nil {
						allowed.Add(1)
					} else if errors.Is(e, nanoflare.ErrSubrequestLimit) {
						denied.Add(1)
					} else {
						t.Errorf("parallel effect: %v", e)
					}
				}()
			}
			wg.Wait()
			if allowed.Load() != 100 || denied.Load() != 20 {
				t.Fatalf("budget: allowed=%d denied=%d", allowed.Load(), denied.Load())
			}
			must(service.FinishDispatch(ctx, callerToken, acquired.Lease, true))
			if _, err = service.AuthorizeRuntime(ctx, acquired.Lease, "kv", a.ID); !errors.Is(err, nanoflare.ErrInvocationExpired) {
				t.Fatalf("cancelled lease accepted: %v", err)
			}
			// Current revocation overrides a pinned policy snapshot.
			ns, err = service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: ns.PolicyRevision, Grants: []nanoflare.NamespaceResourceGrant{}})
			must(err)
			if _, err = service.AuthorizeRuntime(ctx, newer.Lease, "kv", b.ID); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("revoked grant accepted: %v", err)
			}
			// A control-plane restart never revives an invocation handle.
			restarted := nanoflare.NewService(repo, discardWriter{})
			restarted.SetPlatformDispatchEnabled(true)
			if _, err = restarted.AuthorizeRuntime(ctx, newer.Lease, "assets", second.ID); !errors.Is(err, nanoflare.ErrInvocationExpired) {
				t.Fatalf("restart revived lease: %v", err)
			}
			service.SetRuntimeDeploymentChecker(func(context.Context, string, string) (bool, error) { return false, nil })
			if _, err = service.AuthorizeRuntime(ctx, newer.Lease, "assets", second.ID); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("dead caller retained authority: %v", err)
			}
		})
	}
}

func TestDynamicServicesInheritAuthorityAndBudget(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			service := nanoflare.NewService(repo, discardWriter{})
			service.SetPlatformDispatchEnabled(true)
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "production"})
			must(err)
			alpha, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			beta, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "beta", Execution: "dynamic"})
			must(err)
			kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "org", Name: "shared"})
			must(err)
			_, err = service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "kv", ResourceID: kv.ID}}})
			must(err)
			input := nanoflare.DeployInput{Format: "modules", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, CompatibilityDate: "2025-12-10", KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: kv.ID}}}
			first, err := service.Deploy(beta.ID, input)
			must(err)
			parentInput := input
			parentInput.Services = []nanoflare.ServiceBinding{{Binding: "CHILD", WorkerID: beta.ID}}
			_, err = service.Deploy(alpha.ID, parentInput)
			must(err)
			platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "platform", Hostname: "service.example.com"})
			must(err)
			callerInput := input
			callerInput.KVNamespaces = nil
			callerInput.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: ns.ID}}
			caller, err := service.Deploy(platform.ID, callerInput)
			must(err)
			source := nanoflare.ActiveDeployment{App: platform, Deployment: caller}
			token := nanoflare.RuntimeCapability(source, nanoflare.RuntimeDispatch, ns.ID)
			ctx := context.Background()
			root, _, err := service.BeginDispatchRoot(source)
			must(err)
			parent, err := service.AcquireDispatchWithRoot(ctx, token, ns.ID, "alpha", root)
			must(err)
			child, err := service.AcquireServiceDispatch(ctx, token, parent.Lease, "CHILD")
			must(err)
			if child.Deployment.ID != first.ID || child.Deadline.After(parent.Deadline) {
				t.Fatal("service changed selection or enlarged deadline")
			}
			if _, err = service.AcquireServiceDispatch(ctx, token, parent.Lease, "OTHER"); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("undeclared service: %v", err)
			}
			if _, err = service.AcquireServiceDispatch(ctx, "wrong", parent.Lease, "CHILD"); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("substituted transport: %v", err)
			}
			if _, err = service.AuthorizeRuntime(ctx, parent.Lease, nanoflare.RuntimeDispatch, ns.ID); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("service grant broadened to namespace: %v", err)
			}
			_, err = service.DispatchEffect(ctx, token, child.Lease, "kv", kv.ID, 50)
			must(err)
			_, err = service.DispatchEffect(ctx, token, parent.Lease, "kv", kv.ID, 48)
			must(err)
			if _, err = service.DispatchEffect(ctx, token, child.Lease, "kv", kv.ID, 1); !errors.Is(err, nanoflare.ErrSubrequestLimit) {
				t.Fatalf("child bypassed shared root budget: %v", err)
			}
			must(service.FinishDispatch(ctx, token, parent.Lease, true))
			if _, err = service.AuthorizeRuntime(ctx, child.Lease, "kv", kv.ID); !errors.Is(err, nanoflare.ErrInvocationExpired) {
				t.Fatalf("child survived parent cancellation: %v", err)
			}
			// A new child invocation resolves the current target; old invocations stay pinned.
			nextInput := input
			nextInput.CommitMessage = "new child"
			next, err := service.Deploy(beta.ID, nextInput)
			must(err)
			root, _, err = service.BeginDispatchRoot(source)
			must(err)
			parent, err = service.AcquireDispatchWithRoot(ctx, token, ns.ID, "alpha", root)
			must(err)
			newer, err := service.AcquireServiceDispatch(ctx, token, parent.Lease, "CHILD")
			must(err)
			if newer.Deployment.ID != next.ID {
				t.Fatal("service used stale target revision")
			}
			// HTTP selection, namespace dispatch and service fetch use the native weighted
			// selector; every selected invocation must retain one authorized revision.
			must(repo.SetActiveTraffic(beta.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 50}, {ID: next.ID, TrafficPercent: 50}}))
			weightedRoot, _, err := service.BeginDispatchRoot(source)
			must(err)
			weightedParent, err := service.AcquireDispatchWithRoot(ctx, token, ns.ID, "alpha", weightedRoot)
			must(err)
			valid := func(id string) bool { return id == first.ID || id == next.ID }
			for sample := 0; sample < 20; sample++ {
				chosen, e := service.AcquireServiceDispatch(ctx, token, weightedParent.Lease, "CHILD")
				must(e)
				if !valid(chosen.Deployment.ID) {
					t.Fatal("service selected an inactive revision")
				}
				must(service.FinishDispatch(ctx, token, chosen.Lease, true))
				resolved, e := service.ResolveNamespaceArtifactForOrg("org", ns.ID, "beta", "")
				must(e)
				if !valid(resolved.DeploymentID) {
					t.Fatal("namespace resolution selected an inactive revision")
				}
				httpChoice, _, ok, e := service.WorkerRuntimeDeployment(beta.ID, "/")
				must(e)
				if !ok || !valid(httpChoice.Deployment.ID) {
					t.Fatal("HTTP selector selected an inactive revision")
				}
			}
			service.EndDispatchRoot(weightedRoot, true)

			// Namespace and customer boundaries still constrain an exact Worker service binding.
			otherNS, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "other", CustomerMode: "platform"})
			must(err)
			foreign, err := service.CreateNamespaceScript("org", otherNS.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "foreign", CustomerID: "b", Execution: "dynamic"})
			must(err)
			foreignInput := input
			foreignInput.KVNamespaces = nil
			_, err = service.Deploy(foreign.ID, foreignInput)
			must(err)
			deniedInput := parentInput
			deniedInput.Services = []nanoflare.ServiceBinding{{Binding: "FOREIGN", WorkerID: foreign.ID}}
			if _, err = service.Deploy(alpha.ID, deniedInput); !errors.Is(err, nanoflare.ErrNamespaceGrantDenied) {
				t.Fatalf("cross-namespace service: %v", err)
			}
			a, err := service.CreateNamespaceScript("org", otherNS.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "customer-a", CustomerID: "a", Execution: "dynamic"})
			must(err)
			foreignInput.Services = []nanoflare.ServiceBinding{{Binding: "FOREIGN", WorkerID: foreign.ID}}
			if _, err = service.Deploy(a.ID, foreignInput); !errors.Is(err, nanoflare.ErrNamespaceGrantDenied) {
				t.Fatalf("cross-customer service: %v", err)
			}
			// Depth is bounded even for a deliberately recursive same-domain service.
			recursive := input
			recursive.Services = []nanoflare.ServiceBinding{{Binding: "SELF", WorkerID: beta.ID}}
			_, err = service.Deploy(beta.ID, recursive)
			must(err)
			root, _, err = service.BeginDispatchRoot(source)
			must(err)
			current, err := service.AcquireDispatchWithRoot(ctx, token, ns.ID, "beta", root)
			must(err)
			for depth := 0; depth < 8; depth++ {
				current, err = service.AcquireServiceDispatch(ctx, token, current.Lease, "SELF")
				must(err)
			}
			if _, err = service.AcquireServiceDispatch(ctx, token, current.Lease, "SELF"); !errors.Is(err, nanoflare.ErrDispatchDepth) {
				t.Fatalf("recursive dispatch depth: %v", err)
			}
			service.EndDispatchRoot(root, true)
			if _, err = service.AuthorizeRuntime(ctx, current.Lease, "kv", kv.ID); !errors.Is(err, nanoflare.ErrInvocationExpired) {
				t.Fatalf("nested effect survived ingress cancellation: %v", err)
			}
		})
	}
}
