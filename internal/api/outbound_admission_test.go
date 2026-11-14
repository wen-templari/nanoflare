package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestOutboundBindingAdmission(t *testing.T) {
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
			s := nanoflare.NewService(repo, discardWriter{})
			s.SetPlatformDispatchEnabled(true)
			codec, e := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
			must(e)
			s.SetSecretCodec(codec)
			ctx := context.Background()
			ns, err := s.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "customers"})
			must(err)
			customer, err := s.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			input := nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {fetch(){return new Response('ok')}}"}}}
			_, err = s.Deploy(customer.ID, input)
			must(err)
			policy, err := s.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "policy", Hostname: "policy.test"})
			must(err)
			input.Vars = map[string]json.RawMessage{"VERSION": json.RawMessage(`"one"`)}
			must(s.PutSecret(policy.ID, "TOKEN", "pinned-secret"))
			first, err := s.Deploy(policy.ID, input)
			must(err)
			host, err := s.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "host", Hostname: "host.test"})
			must(err)
			config := &nanoflare.DispatchOutbound{WorkerID: policy.ID, Parameters: []string{"context"}}
			input.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "WITH", NamespaceID: ns.ID, Outbound: config}, {Binding: "WITHOUT", NamespaceID: ns.ID}}
			hostRevision, err := s.Deploy(host.ID, input)
			must(err)
			active := nanoflare.ActiveDeployment{App: host, Deployment: hostRevision}
			root, _, err := s.BeginDispatchRoot(active)
			must(err)
			defer s.EndDispatchRoot(root, true)
			token := nanoflare.RuntimeCapability(active, nanoflare.RuntimeDispatch, ns.ID)
			// Completion grace keeps retry authority but must release concurrency capacity.
			for i := 0; i < 40; i++ {
				completed, e := s.AcquireDispatchBinding(ctx, token, ns.ID, "alpha", root, "WITHOUT", nil)
				must(e)
				must(s.FinishDispatch(ctx, token, completed.Lease, false))
			}
			parameters := map[string]json.RawMessage{"context": json.RawMessage(`{"customer":"alpha"}`)}
			acquired, err := s.AcquireDispatchBinding(ctx, token, ns.ID, "alpha", root, "WITH", parameters)
			must(err)
			parameters["context"] = json.RawMessage(`"mutated"`)
			if !acquired.RestrictedOutbound {
				t.Fatal("policy did not restrict native networking")
			}
			for _, test := range []struct {
				name       string
				parameters map[string]json.RawMessage
			}{
				{"", nil}, {"MISSING", nil}, {"WITHOUT", parameters}, {"WITHOUT", map[string]json.RawMessage{}}, {"WITH", map[string]json.RawMessage{"undeclared": json.RawMessage(`true`)}},
			} {
				if _, err = s.AcquireDispatchBinding(ctx, token, ns.ID, "alpha", root, test.name, test.parameters); err == nil {
					t.Fatalf("accepted invalid binding/parameters: %s", test.name)
				}
			}
			input.DispatchNamespaces = nil
			input.Vars["VERSION"] = json.RawMessage(`"two"`)
			_, err = s.Deploy(policy.ID, input)
			must(err)
			must(s.PutSecret(policy.ID, "TOKEN", "new-secret"))
			if _, err = s.AcquireOutboundDispatch(ctx, "wrong-transport", acquired.Lease); err == nil {
				t.Fatal("accepted wrong transport")
			}
			child, err := s.AcquireOutboundDispatch(ctx, token, acquired.Lease)
			must(err)
			if !child.OutboundExecution || !child.RestrictedOutbound || child.Deployment.ID != first.ID || child.Values["TOKEN"] != "pinned-secret" || string(child.OutboundParameters["context"]) != `{"customer":"alpha"}` {
				t.Fatalf("lost pinned outbound snapshot: %+v", child)
			}
			if _, err = s.DispatchEffect(ctx, token, child.Lease, nanoflare.RuntimeOutbound, "", 1); err != nil {
				t.Fatalf("outbound Worker cannot forward: %v", err)
			}
			if err = s.AuthorizeDirectDispatchOutbound(ctx, token, acquired.Lease); err == nil {
				t.Fatal("customer bypassed policy")
			}
			must(s.AuthorizeDirectDispatchOutbound(ctx, token, child.Lease))
			if _, err = s.AcquireOutboundDispatch(ctx, token, child.Lease); err == nil {
				t.Fatal("outbound Worker re-entered itself")
			}
			must(s.FinishDispatch(ctx, token, acquired.Lease, true))
			if _, err = s.DispatchEffect(ctx, token, child.Lease, nanoflare.RuntimeOutbound, "", 1); err == nil {
				t.Fatal("canceled parent left outbound authority alive")
			}
			// Invalid declarations fail at host publication, before runtime execution.
			for _, params := range [][]string{{"context", "context"}, {"VERSION"}, {"TOKEN"}, {"__NANOFLARE_INTERNAL"}, {"__proto__"}} {
				input.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "WITH", NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: policy.ID, Parameters: params}}}
				if _, err = s.Deploy(host.ID, input); err == nil {
					t.Fatalf("accepted invalid parameter declarations: %v", params)
				}
			}
			input.DispatchNamespaces[0].Outbound = &nanoflare.DispatchOutbound{WorkerID: customer.ID}
			if _, err = s.Deploy(host.ID, input); err == nil {
				t.Fatal("accepted customer Worker as outbound target")
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "foreign", Name: "foreign", UsageLevel: nanoflare.UsageLevelPaid}))
			foreign, e := s.CreateApp(nanoflare.CreateAppInput{OrgID: "foreign", Name: "foreign", Hostname: "foreign.test"})
			must(e)
			input.DispatchNamespaces[0].Outbound = &nanoflare.DispatchOutbound{WorkerID: foreign.ID}
			if _, err = s.Deploy(host.ID, input); err == nil {
				t.Fatal("accepted foreign outbound target")
			}
			input.DispatchNamespaces[0].Outbound = &nanoflare.DispatchOutbound{WorkerID: host.ID}
			if _, err = s.Deploy(host.ID, input); err == nil {
				t.Fatal("accepted self reference")
			}
			pending, e := s.AcquireDispatchBinding(ctx, token, ns.ID, "alpha", root, "WITH", nil)
			must(e)
			must(s.DeleteAppForOrg("org", policy.ID))
			replacement, e := s.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "policy", Hostname: "policy.test"})
			must(e)
			if replacement.ID == policy.ID {
				t.Fatal("test reused immutable target identity")
			}
			if _, e = s.AcquireOutboundDispatch(ctx, token, pending.Lease); e == nil {
				t.Fatal("deleted target silently fell back or rebound")
			}
			if _, e = s.AcquireDispatchBinding(ctx, token, ns.ID, "alpha", root, "WITH", nil); e == nil {
				t.Fatal("new admission accepted deleted target")
			}

		})
	}
}
