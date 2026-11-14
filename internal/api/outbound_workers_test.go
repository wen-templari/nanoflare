package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestOutboundWorkersEndToEnd(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_WORKERD") == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to pinned workerd")
	}
	for _, dynamic := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("dynamic=%t/remote=%t", dynamic, remote), func(t *testing.T) {
				output := runtime.NewOutputBuffer()
				canceled := make(chan string, 4)
				resolver, _ := privateDNSFixture(t)
				privateDynamicDispatchWithFixture(t, remote, dynamic, func(server *Server, service *nanoflare.Service, org nanoflare.Organization, _ string) {
					must := func(err error) {
						t.Helper()
						if err != nil {
							t.Fatal(err)
						}
					}
					ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "outbound"})
					must(err)
					customer, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
					must(err)
					defer func() {
						if t.Failed() {
							t.Logf("customer logs: %+v", output.Output(customer.ID))
						}
					}()
					policyKV := map[string]string{}
					policyServices := map[string][]nanoflare.ServiceBinding{}
					policyDatabases := map[string]string{}
					policyBuckets := map[string]string{}
					deploy := func(id, code string, bindings []nanoflare.DispatchBinding) nanoflare.Deployment {
						t.Helper()
						var kv []nanoflare.KVBinding
						if namespace := policyKV[id]; namespace != "" {
							kv = []nanoflare.KVBinding{{Binding: "STATE", ID: namespace}}
						}
						dns := nanoflare.DNSSelection{}
						var assets []nanoflare.AssetFile
						var databases []nanoflare.DatabaseBinding
						var buckets []nanoflare.ObjectStorageBucketBinding
						if id := policyDatabases[id]; id != "" {
							databases = []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: id}}
						}
						if id := policyBuckets[id]; id != "" {
							buckets = []nanoflare.ObjectStorageBucketBinding{{Binding: "BUCKET", BucketID: id}}
						}
						flags := []string(nil)
						if policyKV[id] != "" {
							dns.Profile = "corporate"
							assets = []nanoflare.AssetFile{{Path: "ok", Data: []byte("explicit-policy-asset")}}
							flags = []string{"nodejs_compat"}
						}
						d, e := service.Deploy(id, nanoflare.DeployInput{Databases: databases, ObjectStorageBuckets: buckets, Assets: assets, DNS: dns, CompatibilityFlags: flags, Services: policyServices[id], KVNamespaces: kv, Format: "modules", CompatibilityDate: "2026-09-11", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: code}}, DispatchNamespaces: bindings})
						must(e)
						return d
					}
					leaf, e := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "leaf", Execution: "dynamic"})
					must(e)
					deploy(leaf.ID, `export default {fetch(){return new Response('nested-dispatch-ok')}}`, nil)
					deploy(customer.ID, `import {connect} from 'cloudflare:sockets';export default {async fetch(request){
 const url=new URL(request.url);
 if(url.searchParams.has('exception'))return fetch('http://8.8.8.8/exception');
 if(url.searchParams.has('background'))return fetch('http://8.8.8.8/background');
 if(url.searchParams.has('background-status'))return fetch('http://8.8.8.8/background-status');
 if(url.searchParams.has('slow'))return fetch('http://8.8.8.8/slow?probe=policy');
 if(url.searchParams.has('binary'))return fetch(new Request('http://8.8.8.8/outbound-binary',request));
 if(url.searchParams.has('redirect'))return fetch('http://8.8.8.8/outbound-redirect?status='+url.searchParams.get('redirect'),{method:request.method,headers:request.headers,body:await request.arrayBuffer(),redirect:'follow'});
 if(url.searchParams.has('tcp')){try{const socket=connect('127.0.0.1:1');await socket.opened;await socket.close();return new Response('bypass');}catch{return new Response('tcp-denied',{status:403});}}
 return fetch('http://8.8.8.8/'+(url.searchParams.has('block')?'blocked':'ok'),{headers:{'X-Customer':url.searchParams.get('customer')}});
}};`, nil)
					var bindings []nanoflare.DispatchBinding
					for _, tag := range []string{"A", "B"} {
						policy, e := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "policy-" + tag, Hostname: "policy-" + tag + ".test"})
						must(e)
						defer func() {
							if t.Failed() {
								t.Logf("policy logs: %+v", output.Output(policy.ID))
							}
						}()
						kv, e := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: org.ID, Name: "policy-" + tag})
						must(e)
						policyKV[policy.ID] = kv.ID
						database, e := service.CreateDatabase(nanoflare.CreateDatabaseInput{OrgID: org.ID, Name: "policy-" + tag})
						must(e)
						policyDatabases[policy.ID] = database.ID
						bucket, e := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{OrgID: org.ID, Name: "policy-" + tag})
						must(e)
						policyBuckets[policy.ID] = bucket.ID
						must(service.PutSecret(policy.ID, "TOKEN", "secret-"+tag))
						helper, e := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "helper-" + tag, Hostname: "helper-" + tag + ".test"})
						must(e)
						must(service.PutSecret(helper.ID, "OWN_SECRET", "helper-"+tag))
						deploy(helper.ID, `import {WorkerEntrypoint} from 'cloudflare:workers';export default class extends WorkerEntrypoint {fetch(){return new Response(this.env.OWN_SECRET)} echo(value){return this.env.OWN_SECRET+':'+value;}}`, nil)
						policyServices[policy.ID] = []nanoflare.ServiceBinding{{Binding: "HELPER", WorkerID: helper.ID}}

						code := `import {lookup} from 'node:dns/promises';import {env as importedEnv} from 'cloudflare:workers';export default {async fetch(request,env,ctx){
 if(new URL(request.url).pathname==='/exception')throw new Error('outbound-policy-exception-fixture');
 if(new URL(request.url).pathname==='/background'){ctx.waitUntil((async()=>{await new Promise(resolve=>setTimeout(resolve,2300));await env.STATE.put('background',env.TOKEN)})());return new Response('scheduled')}
 if(new URL(request.url).pathname==='/background-status')return new Response(await env.STATE.get('background')||'pending');
 if(new URL(request.url).pathname==='/slow')return fetch(request);
 if(new URL(request.url).pathname.startsWith('/outbound-')){const response=await fetch(request,{redirect:'manual'});const headers=new Headers(response.headers);headers.set('X-Outbound-Policy',env.TOKEN);return new Response(response.body,{status:response.status,headers})}
 if(new URL(request.url).pathname==='/blocked')return new Response('blocked',{status:403});
 await new Promise(resolve=>setTimeout(resolve,5));
 if(importedEnv.context.customer!==env.context.customer)throw new Error('parameter environment mismatch');
 if(env.optionalContext!==undefined||importedEnv.optionalContext!==undefined)throw new Error('missing parameter must be undefined');
 const row=await env.DB.prepare('SELECT ? AS token').bind(env.TOKEN).first();if(row.token!==env.TOKEN)throw new Error('outbound D1 binding failed');
 await env.BUCKET.put(env.context.customer,env.TOKEN);const object=await env.BUCKET.get(env.context.customer);if(await object.text()!==env.TOKEN)throw new Error('outbound R2 binding failed');
 const asset=await env.ASSETS.fetch('http://assets/ok');if(await asset.text()!=='explicit-policy-asset')throw new Error('explicit outbound assets binding failed');
 const resolved=await lookup('database.corporate.invalid');if(resolved.address!=='10.20.30.40')throw new Error('outbound DNS mismatch');
 await env.STATE.put(env.context.customer,env.TOKEN);
 if(await env.STATE.get(env.context.customer)!==env.TOKEN)throw new Error("outbound storage binding failed");
 const nested=await env.INNER.get('leaf').fetch('http://leaf/');
 if(await nested.text()!=='nested-dispatch-ok')throw new Error('outbound own dispatch binding failed');
 const helper=await env.HELPER.fetch('http://helper/');
 if(await helper.text()!==env.TOKEN.replace('secret-','helper-'))throw new Error('service fetch authority mismatch');
 if(await env.HELPER.echo(env.context.customer)!==env.TOKEN.replace('secret-','helper-')+':'+env.context.customer)throw new Error('service RPC authority mismatch');
 const response=await fetch(request,{headers:{Authorization:env.TOKEN}});
 return Response.json({tag:env.TOKEN,customer:env.context.customer,origin:await response.text()});
}};`
						if tag == "B" {
							code = strings.Replace(code, `import {env as importedEnv}`, `import {WorkerEntrypoint,env as importedEnv}`, 1)
							code = strings.Replace(code, `export default {async fetch(request,env,ctx){`, `export class Policy extends WorkerEntrypoint {constructor(ctx,env){super(ctx,env);if(importedEnv.context.customer!==env.context.customer)throw new Error("constructor parameter mismatch");}async fetch(request){const env=this.env,ctx=this.ctx;`, 1)
						}
						if tag == "B" {
							code += "\nexport class Invalid {}\nexport default {fetch(){throw new Error('wrong outbound entrypoint')}};"
						}
						deploy(policy.ID, code, []nanoflare.DispatchBinding{{Binding: "INNER", NamespaceID: ns.ID}})
						entrypoint := ""
						if tag == "B" {
							entrypoint = "Policy"
						}
						bindings = append(bindings, nanoflare.DispatchBinding{Binding: tag, NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: policy.ID, Entrypoint: entrypoint, Parameters: []string{"context", "optionalContext"}}})
						if tag == "B" {
							for _, selector := range []string{"Missing", "Invalid"} {
								bindings = append(bindings, nanoflare.DispatchBinding{Binding: selector, NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: policy.ID, Entrypoint: selector}})
							}
						}
					}
					blockPolicy, e := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "block-policy", Hostname: "block-policy.test"})
					must(e)
					deploy(blockPolicy.ID, `export default {fetch(){return new Response('all-blocked',{status:403})}}`, nil)
					bindings = append(bindings, nanoflare.DispatchBinding{Binding: "BLOCK", NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: blockPolicy.ID}})
					bindings = append(bindings, nanoflare.DispatchBinding{Binding: "NONE", NamespaceID: ns.ID})
					host, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "outbound-host", Hostname: "outbound.test"})
					must(err)
					hostRevision := deploy(host.ID, `export default {fetch(request,env){
 const url=new URL(request.url),name=url.searchParams.get('binding')||'A';
 const context={customer:url.searchParams.get('customer')};
 const target=env[name].get('alpha',{},name==='A'||name==='B'?{outbound:{context}}:{});
 context.customer='mutated-after-get';
 return target.fetch(request);
}};`, bindings)
					// Even a caller that knows its own transport token cannot bypass the
					// hook by using the direct network endpoint with a policy-bound lease.
					token := nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: host, Deployment: hostRevision}, nanoflare.RuntimeDispatch, ns.ID)
					artifact, e := service.AcquireDispatchBinding(context.Background(), token, ns.ID, "alpha", "", "A", nil)
					must(e)
					if e = service.AuthorizeDirectDispatchOutbound(context.Background(), token, artifact.Lease); e == nil {
						t.Fatal("direct outbound bypass allowed")
					}
					must(service.FinishDispatch(context.Background(), token, artifact.Lease, true))
					public := httptest.NewServer(server)
					defer public.Close()
					get := func(query string) (int, string, error) {
						response, e := public.Client().Get(public.URL + "/internal/http/workers/" + host.ID + "/?" + query)
						if e != nil {
							return 0, "", e
						}
						defer response.Body.Close()
						body, e := io.ReadAll(response.Body)
						return response.StatusCode, string(body), e
					}
					var wg sync.WaitGroup
					for i := 0; i < 4; i++ {
						wg.Add(1)
						go func(i int) {
							defer wg.Done()
							tag := []string{"A", "B"}[i%2]
							customer := fmt.Sprintf("c%d", i)
							status, body, e := get("binding=" + tag + "&customer=" + customer)
							if e != nil {
								t.Error(e)
								return
							}
							var result map[string]string
							if e = json.Unmarshal([]byte(body), &result); e != nil || status != 200 || result["tag"] != "secret-"+tag || result["origin"] != "secret-"+tag || result["customer"] != customer {
								t.Errorf("concurrent outbound: %d %s (%v)", status, body, e)
							}
						}(i)
					}
					wg.Wait()
					for _, mode := range []string{"binary=1", "redirect=302", "redirect=307"} {
						payload := []byte{0, 255, 128, 1, 13, 10}
						request, e := http.NewRequest("POST", public.URL+"/internal/http/workers/"+host.ID+"/?customer=binary&"+mode, bytes.NewReader(payload))
						must(e)
						request.Header.Set("Authorization", "Bearer customer")
						response, e := public.Client().Do(request)
						must(e)
						body, e := io.ReadAll(response.Body)
						response.Body.Close()
						must(e)
						want := payload
						method := "POST"
						auth := "Bearer customer"
						if mode == "redirect=302" {
							want = nil
							method = "GET"
						}
						if strings.HasPrefix(mode, "redirect=") {
							auth = ""
						}
						if response.StatusCode != 200 || !bytes.Equal(body, want) || response.Header.Get("X-Origin-Method") != method || response.Header.Get("X-Origin-Authorization") != auth || response.Header.Get("X-Outbound-Policy") != "secret-A" {
							t.Fatalf("production HTTP %s: status=%d body=%v headers=%v", mode, response.StatusCode, body, response.Header)
						}
					}
					for _, tag := range []string{"A", "B"} {
						status, _, e := get("binding=" + tag + "&exception=1&customer=error")
						must(e)
						if status != http.StatusInternalServerError {
							t.Fatalf("policy exception did not fail closed: %s %d", tag, status)
						}
					}
					for _, tag := range []string{"A", "B"} {
						status, body, e := get("binding=" + tag + "&background=1&customer=bg")
						must(e)
						if status != 200 || body != "scheduled" {
							t.Fatalf("background admission: %d %s", status, body)
						}
					}
					time.Sleep(2600 * time.Millisecond)
					deadline := time.Now().Add(5 * time.Second)
					for _, tag := range []string{"A", "B"} {
						for {
							status, body, e := get("binding=" + tag + "&background-status=1&customer=bg")
							must(e)
							if status == 200 && body == "secret-"+tag {
								break
							}
							if time.Now().After(deadline) {
								t.Fatalf("ordinary outbound waitUntil lost authority: %s %d %s", tag, status, body)
							}
							time.Sleep(100 * time.Millisecond)
						}
					}
					cancelContext, cancel := context.WithCancel(context.Background())
					slowRequest, e := http.NewRequestWithContext(cancelContext, "GET", public.URL+"/internal/http/workers/"+host.ID+"/?slow=1&customer=slow", nil)
					must(e)
					slowResponse, e := public.Client().Do(slowRequest)
					must(e)
					first := make([]byte, len("outbound-start\n"))
					_, e = io.ReadFull(slowResponse.Body, first)
					must(e)
					if string(first) != "outbound-start\n" {
						t.Fatalf("policy stream: %q", first)
					}
					cancel()
					slowResponse.Body.Close()
					select {
					case probe := <-canceled:
						if probe != "policy" {
							t.Fatalf("cancellation probe: %s", probe)
						}
					case <-time.After(2 * time.Second):
						t.Fatal("outbound policy origin survived root disconnect")
					}
					for _, query := range []string{"block=1", "tcp=1"} {
						status, body, e := get(query)
						must(e)
						if status != 403 {
							t.Fatalf("%s: %d %s", query, status, body)
						}
					}
					status, body, e := get("binding=BLOCK")
					must(e)
					if status != 403 || body != "all-blocked" {
						t.Fatalf("outbound without parameters: %d %s", status, body)
					}
					for _, selector := range []string{"Missing", "Invalid"} {
						status, body, e = get("binding=" + selector)
						must(e)
						if status != 500 {
							t.Fatalf("invalid outbound selector %s: %d %s", selector, status, body)
						}
						found := false
						for _, line := range output.Output(customer.ID) {
							if strings.Contains(line.Message, "Invalid outbound fetch entrypoint: "+selector) {
								found = true
							}
						}
						if !found {
							t.Fatalf("missing explicit invalid selector diagnostic for %s: %+v", selector, output.Output(customer.ID))
						}
					}
					status, body, e = get("binding=NONE")
					must(e)
					if status != 200 || body != "" {
						t.Fatalf("no-policy regression: %d %s", status, body)
					}
				}, dispatchFixtureObservations{Output: output, OutboundCanceled: func(probe string) {
					select {
					case canceled <- probe:
					default:
					}
				}, DNS: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Nanoflare-DNS-Profile") != "corporate" {
						http.Error(w, "wrong outbound DNS authority", 403)
						return
					}
					resolver.ServeHTTP(w, r)
				})})
			})
		}
	}
}
