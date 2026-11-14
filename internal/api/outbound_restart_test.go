package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestOutboundWorkersPostgresEndToEnd(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_POSTGRES_URL") == "" {
		t.Skip("disposable PostgreSQL required")
	}
	t.Setenv("NANOFLARE_TEST_RUNTIME_BACKEND", "postgres")
	TestOutboundWorkersEndToEnd(t)
}

func TestOutboundRunnerRestart(t *testing.T) { outboundRunnerRestart(t) }
func TestOutboundRunnerPostgresRestart(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_POSTGRES_URL") == "" {
		t.Skip("disposable PostgreSQL required")
	}
	t.Setenv("NANOFLARE_TEST_RUNTIME_BACKEND", "postgres")
	outboundRunnerRestart(t)
}
func outboundRunnerRestart(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_WORKERD") == "" {
		t.Skip("pinned native workerd required")
	}
	for _, dynamic := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("dynamic=%t/remote=%t", dynamic, remote), func(t *testing.T) {
				var launcher *dispatchCountingLauncher
				var manager RuntimeEnsurer
				var restartRunner func() (string, string, error)
				privateDynamicDispatchWithFixture(t, remote, dynamic, func(server *Server, service *nanoflare.Service, org nanoflare.Organization, _ string) {
					must := func(err error) {
						t.Helper()
						if err != nil {
							t.Fatal(err)
						}
					}
					deploy := func(id, code string, bindings []nanoflare.DispatchBinding, kv []nanoflare.KVBinding) {
						_, err := service.Deploy(id, nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: code}}, DispatchNamespaces: bindings, KVNamespaces: kv})
						must(err)
					}
					ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "restart-customers"})
					must(err)
					customer, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "customer", Execution: "dynamic"})
					must(err)
					must(service.PutSecret(customer.ID, "TOKEN", "customer-secret"))
					deploy(customer.ID, `export default {fetch(request){return fetch("https://restart-fixture.invalid/")}}`, nil, nil)
					policy, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "restart-policy", Hostname: "restart-policy.test"})
					must(err)
					must(service.PutSecret(policy.ID, "TOKEN", "policy-secret"))
					kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: org.ID, Name: "restart-policy-state"})
					must(err)
					deploy(policy.ID, `import {WorkerEntrypoint,env as importedEnv} from "cloudflare:workers";export class Policy extends WorkerEntrypoint {async fetch(){const count=Number(await this.env.STATE.get("counter")??0)+1;await this.env.STATE.put("counter",String(count));return Response.json({count,context:this.env.context,imported:importedEnv.context,secret:this.env.TOKEN});}};export default {fetch(){return new Response("wrong export",{status:500})}}`, nil, []nanoflare.KVBinding{{Binding: "STATE", ID: kv.ID}})
					host, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "restart-host", Hostname: "restart-host.test"})
					must(err)
					deploy(host.ID, `export default {fetch(request,env){return env.CUSTOMERS.get("customer",{},{outbound:{context:{marker:request.headers.get("x-marker")}}}).fetch(request)}}`, []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: policy.ID, Entrypoint: "Policy", Parameters: []string{"context"}}}}, nil)
					public := httptest.NewServer(server)
					defer public.Close()
					invoke := func(marker string, count int) {
						t.Helper()
						request, err := http.NewRequest("GET", public.URL+"/internal/http/workers/"+host.ID+"/", nil)
						must(err)
						request.Header.Set("X-Marker", marker)
						response, err := public.Client().Do(request)
						must(err)
						defer response.Body.Close()
						data, err := io.ReadAll(response.Body)
						must(err)
						if response.StatusCode != 200 {
							t.Fatalf("invoke status%d: %s", response.StatusCode, data)
						}
						var got struct {
							Count             int
							Context, Imported map[string]string
							Secret            string
						}
						must(json.Unmarshal(data, &got))
						if got.Count != count || got.Context["marker"] != marker || got.Imported["marker"] != marker || got.Secret != "policy-secret" {
							t.Fatalf("incorrect policy authority/state after restart: %s", data)
						}
					}
					invoke("before-restart", 1)
					launcher.mu.Lock()
					process := launcher.processes[len(launcher.processes)-1]
					started := launcher.count.Load()
					launcher.mu.Unlock()
					ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
					_ = process.Stop(ctx)
					cancel()
					source, _, found, err := service.WorkerRuntimeDeployment(host.ID, "/")
					must(err)
					if !found {
						t.Fatal("retained host missing")
					}
					deadline := time.Now().Add(8 * time.Second)
					for {
						endpoint, err := manager.Ensure(context.Background(), source)
						if err == nil {
							endpoint.Release()
						}
						if err == nil && launcher.count.Load() > started {
							break
						}
						if time.Now().After(deadline) {
							t.Fatalf("runner failed to recover: %v", err)
						}
						time.Sleep(10 * time.Millisecond)
					}
					invoke("after-restart", 2)
					if remote {
						before, after, err := restartRunner()
						must(err)
						if before == "" || before == after {
							t.Fatal("runner boot identity did not change")
						}
						must(service.ReconcileRuntime())
						invoke("after-runner-reboot", 3)
						t.Log("runner HTTP server and runtime manager recreated at same address; boot identity changed and production reconciliation restored retained policy")
					}
					t.Log("native child replaced; retained outbound named selector, request parameters, imported env, own secret and KV state survived")
				}, dispatchFixtureObservations{RunnerRestart: func(restart func() (string, string, error)) { restartRunner = restart }, RuntimeControl: func(l *dispatchCountingLauncher, m RuntimeEnsurer) { launcher = l; manager = m }})
			})
		}
	}
}
