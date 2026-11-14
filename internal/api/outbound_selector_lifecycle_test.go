package api

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// Admission-only qualification: executing the selected export is covered by the
// production runtime matrix. Here all assertions run against both repositories.
func TestOutboundSelectorLifecycle(t *testing.T) {
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
			s, _ := managedService(t, repo, discardWriter{})
			ctx := context.Background()
			ns, err := s.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "customers"})
			must(err)
			customer, err := s.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			code := nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "import {WorkerEntrypoint} from 'cloudflare:workers';export class Policy extends WorkerEntrypoint {fetch(){return new Response('named')}};export default {fetch(){return new Response('default')}}"}}}
			_, err = s.Deploy(customer.ID, code)
			must(err)
			policy, err := s.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "policy", Hostname: "policy.test"})
			must(err)
			must(s.PutSecret(policy.ID, "TOKEN", "first-secret"))
			first, err := s.Deploy(policy.ID, code)
			must(err)
			must(s.PutSecret(policy.ID, "TOKEN", "second-secret"))
			code.Vars = map[string]json.RawMessage{"VERSION": json.RawMessage(`"second"`)}
			second, err := s.Deploy(policy.ID, code)
			must(err)
			host, err := s.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "host", Hostname: "host.test"})
			must(err)
			hostCode := code
			hostCode.Vars = nil
			hostCode.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: policy.ID, Parameters: []string{"context"}}}}
			base, err := s.Deploy(host.ID, hostCode)
			must(err)
			selected := nanoflare.CloneDispatchBindings(hostCode.DispatchNamespaces)
			selected[0].Outbound.Entrypoint = "Policy"
			update, err := s.UpdateDispatchBindingsForOrg("org", host.ID, nanoflare.UpdateDispatchBindingsInput{DispatchNamespaces: selected, ExpectedDeploymentID: base.ID, IdempotencyKey: "select-policy"})
			must(err)
			hydrated, _, found, err := s.WorkerRuntimeDeployment(host.ID, "/")
			must(err)
			if !found || !reflect.DeepEqual(hydrated.Deployment.DispatchNamespaces, selected) {
				t.Fatal("retained selector lost")
			}
			frozenBase, err := nanoflare.FreezeExecution(nanoflare.ActiveDeployment{App: host, Deployment: base})
			must(err)
			baseCopy := base
			baseCopy.DispatchNamespaces = selected
			frozenSelected, err := nanoflare.FreezeExecution(nanoflare.ActiveDeployment{App: host, Deployment: baseCopy})
			must(err)
			if frozenBase.ID() == frozenSelected.ID() {
				t.Fatal("selector absent from execution identity")
			}
			readSelector := func(want string) {
				t.Helper()
				bindings, e := s.ActiveWorkerBindingsForOrg("org", host.ID)
				must(e)
				if len(bindings) != 1 || len(bindings[0].DispatchNamespaces) != 1 || bindings[0].DispatchNamespaces[0].Outbound.Entrypoint != want {
					t.Fatalf("active selector readback: %+v", bindings)
				}
			}
			readSelector("Policy")
			_, err = s.SetDeploymentTrafficForOrg("org", host.ID, []nanoflare.DeploymentTraffic{{ID: base.ID, TrafficPercent: 100}})
			must(err)
			readSelector("")
			_, err = s.SetDeploymentTrafficForOrg("org", host.ID, []nanoflare.DeploymentTraffic{{ID: update.ID, TrafficPercent: 100}})
			must(err)
			readSelector("Policy")
			hydrated, _, _, err = s.WorkerRuntimeDeployment(host.ID, "/")
			must(err)
			root, _, err := s.BeginDispatchRoot(hydrated)
			must(err)
			defer s.EndDispatchRoot(root, true)
			token := nanoflare.RuntimeCapability(hydrated, nanoflare.RuntimeDispatch, ns.ID)
			acquire := func() nanoflare.DispatchArtifact {
				t.Helper()
				a, e := s.AcquireDispatchBinding(ctx, token, ns.ID, "alpha", root, "CUSTOMERS", map[string]json.RawMessage{"context": json.RawMessage(`{"customer":"alpha"}`)})
				must(e)
				return a
			}
			// Force each endpoint once, then exercise a real split without asserting a
			// statistically fragile proportion. Every selected snapshot must be coherent.
			for _, revision := range []string{first.ID, second.ID} {
				_, err = s.SetDeploymentTrafficForOrg("org", policy.ID, []nanoflare.DeploymentTraffic{{ID: revision, TrafficPercent: 100}})
				must(err)
				admitted := acquire()
				child, e := s.AcquireOutboundDispatch(ctx, token, admitted.Lease)
				must(e)
				if child.Deployment.ID != revision || child.OutboundEntrypoint != "Policy" {
					t.Fatalf("wrong target/selector: %+v", child)
				}
				must(s.FinishDispatch(ctx, token, admitted.Lease, false))
			}
			_, err = s.SetDeploymentTrafficForOrg("org", policy.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 30}, {ID: second.ID, TrafficPercent: 70}})
			must(err)
			pending := make([]nanoflare.DispatchArtifact, 0, 12)
			for i := 0; i < 12; i++ {
				pending = append(pending, acquire())
			}
			// Update both code and secrets after parent admission. Pending child policy
			// invocations must continue to use one of the admitted traffic snapshots.
			must(s.PutSecret(policy.ID, "TOKEN", "third-secret"))
			code.Vars["VERSION"] = json.RawMessage(`"third"`)
			third, e := s.Deploy(policy.ID, code)
			must(e)
			for _, admitted := range pending {
				child, e := s.AcquireOutboundDispatch(ctx, token, admitted.Lease)
				must(e)
				switch child.Deployment.ID {
				case first.ID:
					if child.Values["TOKEN"] != "first-secret" || child.Values["VERSION"] != nil {
						t.Fatalf("mixed first snapshot: %+v", child.Values)
					}
				case second.ID:
					if child.Values["TOKEN"] != "second-secret" || child.Values["VERSION"] != "second" {
						t.Fatalf("mixed second snapshot: %+v", child.Values)
					}
				default:
					t.Fatalf("pending admission selected later revision %s (latest %s)", child.Deployment.ID, third.ID)
				}
				must(s.FinishDispatch(ctx, token, admitted.Lease, false))
			}
			// A repository target rename retains immutable authority.
			renamed, err := repo.GetApp(policy.ID)
			must(err)
			renamed.Name = "renamed-policy"
			must(repo.UpdateApp(renamed))
			admitted := acquire()
			child, e := s.AcquireOutboundDispatch(ctx, token, admitted.Lease)
			must(e)
			if child.Deployment.AppID != policy.ID {
				t.Fatal("rename changed target identity")
			}
			must(s.FinishDispatch(ctx, token, admitted.Lease, false))
			// Removing all traffic revokes new target selection rather than choosing an
			// inactive revision or invoking the customer without policy.
			must(repo.SetActiveTraffic(policy.ID, nil))
			if _, e = s.AcquireDispatchBinding(ctx, token, ns.ID, "alpha", root, "CUSTOMERS", nil); e == nil {
				t.Fatal("inactive policy accepted new admission")
			}
		})
	}
}
